// Geometry and winding follow skia-pathops 0.9.2 (BSD-3-Clause).
// See THIRD_PARTY_NOTICES.txt. The public boundary contains no Python objects.
#include "bridge.h"
#include "include/core/SkPath.h"
#include "include/core/SkPathBuilder.h"
#include "include/core/SkPathIter.h"
#include "include/core/SkPaint.h"
#include "include/core/SkPathUtils.h"
#include "include/pathops/SkPathOps.h"
#include <algorithm>
#include <cmath>
#include <cstdlib>
#include <cstring>
#include <stdexcept>
#include <vector>

namespace {
using Commands = std::vector<HDCommand>;
int points(uint32_t verb) {
    static const int counts[] = {1, 1, 2, 3, 0};
    if (verb > 4) throw std::runtime_error("verb");
    return counts[verb];
}
SkPoint point(const HDCommand& c, int i) { return {c.points[2*i], c.points[2*i+1]}; }
HDCommand command(uint32_t verb, SkPoint a = {}, SkPoint b = {}, SkPoint c = {}) {
    return {verb, {a.x(), a.y(), b.x(), b.y(), c.x(), c.y()}};
}
void append(SkPathBuilder& p, const HDCommand& c) {
    switch(c.verb) {
    case 0: p.moveTo(point(c,0)); break;
    case 1: p.lineTo(point(c,0)); break;
    case 2: p.quadTo(point(c,0),point(c,1)); break;
    case 3: p.cubicTo(point(c,0),point(c,1),point(c,2)); break;
    case 4: p.close(); break;
    default: throw std::runtime_error("verb");
    }
}
SkPath path(const HDCommand* commands, size_t count) {
    SkPathBuilder p;
    for (size_t i=0; i<count; ++i) {
        for(int j=0;j<points(commands[i].verb)*2;++j)
            if(!std::isfinite(commands[i].points[j])) throw std::runtime_error("coordinate");
        append(p,commands[i]);
    }
    return p.detach();
}
Commands encode(const SkPath& p) {
    Commands out;
    auto it=p.iter();
    while(auto r=it.next()) {
        auto v=r->fPoints;
        switch(r->fVerb) {
        case SkPathVerb::kMove: out.push_back(command(0,v[0])); break;
        case SkPathVerb::kLine: out.push_back(command(1,v[1])); break;
        case SkPathVerb::kQuad: out.push_back(command(2,v[1],v[2])); break;
        case SkPathVerb::kCubic: out.push_back(command(3,v[1],v[2],v[3])); break;
        case SkPathVerb::kClose: out.push_back(command(4)); break;
        case SkPathVerb::kConic: {
            float a=r->fConicWeight-1, k=a/(4*(2+a));
            float x=k*(v[0].x()-2*v[1].x()+v[2].x());
            float y=k*(v[0].y()-2*v[1].y()+v[2].y());
            float error=std::sqrt(x*x+y*y);
            // Match the upstream Python range(5): maximum returned power is 4.
            int pow=0;
            while(pow<4 && error>.1f) {error*=.25f; ++pow;}
            SkPoint q[65];
            int n=SkPath::ConvertConicToQuads(v[0],v[1],v[2],r->fConicWeight,q,pow);
            for(int i=0;i<n;++i) out.push_back(command(2,q[2*i+1],q[2*i+2]));
            break;
        }
        }
    }
    return out;
}
SkPath path(const Commands& c) {return path(c.data(),c.size());}
SkPoint end(const HDCommand& c) {return point(c,points(c.verb)-1);}
double area(const Commands& c) {
    double sum=0;
    SkPoint start{},last{};
    bool open=false;
    auto line=[&](SkPoint to) {sum-=(to.x()-last.x())*(to.y()+last.y())*.5; last=to;};
    for(const auto& v:c) {
        if(v.verb==0) {if(open)line(start);start=last=point(v,0);open=true;}
        else if(v.verb==4) {line(start);open=false;}
        else {
            if(v.verb==2) {
                float x1=v.points[0]-last.x(), y1=v.points[1]-last.y();
                float x2=v.points[2]-last.x(), y2=v.points[3]-last.y();
                sum-=(x2*y1-x1*y2)/3.0;
            } else if(v.verb==3) {
                float x1=v.points[0]-last.x(), y1=v.points[1]-last.y();
                float x2=v.points[2]-last.x(), y2=v.points[3]-last.y();
                float x3=v.points[4]-last.x(), y3=v.points[5]-last.y();
                sum-=(x1*(-y2-y3)+x2*(y1-2*y3)+x3*(y1+2*y2))*.15;
            }
            line(end(v));
        }
    }
    if(open)line(start);
    return sum;
}
Commands reverse(const Commands& c) {
    std::vector<SkPoint> starts;
    SkPoint last{};
    bool closed=false;
    for(const auto& v:c) {
        starts.push_back(last);
        if(v.verb==4)closed=true;else last=end(v);
    }
    Commands out{command(0,last)};
    for(size_t i=c.size();i-->1;) {
        const auto& v=c[i];
        if(v.verb==1)out.push_back(command(1,starts[i]));
        if(v.verb==2)out.push_back(command(2,point(v,0),starts[i]));
        if(v.verb==3)out.push_back(command(3,point(v,1),point(v,0),starts[i]));
    }
    if(closed)out.push_back(command(4));
    return out;
}
// Matches the pinned skia-pathops 0.9.2 winding used by version 1.3.
bool inside(const SkPath& outer,const Commands& inner,const SkPath& innerpath) {
    auto a=outer.computeTightBounds(), b=innerpath.computeTightBounds();
    if(!SkRect::Intersects(a,b))return false;
    for(const auto& v:inner) if(v.verb!=4) {
        auto p=end(v);
        if(!outer.contains(p.x(),p.y()))return false;
    }
    return true;
}
// Restoring the contour start avoids unstable serialization on repeated builds.
bool near(SkPoint a,SkPoint b) {
    float dx=a.x()-b.x(),dy=a.y()-b.y();
    return dx*dx+dy*dy<=1.0f/(4096*4096);
}
bool collinear(SkPoint a,SkPoint b,SkPoint c) {
    return std::fabs(a.x()*(b.y()-c.y())+b.x()*(c.y()-a.y())+c.x()*(a.y()-b.y()))<=2.0f/4096;
}
void restore(Commands& c,std::vector<SkPoint>& starts) {
    if(c.empty())return;
    bool closed=c.back().verb==4;
    for(size_t j=0;j<starts.size();++j) {
        size_t found=c.size();
        for(size_t i=0;i<c.size();++i)if(c[i].verb!=4 && end(c[i])==starts[j]) {found=i;break;}
        if(found==c.size() || found==0)continue;
        if(!closed) {
            if(found+1!=c.size())continue;
            c=reverse(c);
        } else {
            Commands rotated{command(0,starts[j])};
            SkPoint last=starts[j],first=point(c[0],0);
            for(size_t step=1;step<c.size();++step) {
                size_t i=(found+step)%c.size();
                const auto& v=c[i];
                if(v.verb==0) {
                    if(!near(last,first) && !(c.size()>1 && c[1].verb==1 && collinear(last,first,end(c[1])))) {
                        rotated.push_back(command(1,first));last=first;
                    }
                } else if(v.verb==1) {
                    if(!(c[(i+1)%c.size()].verb==4 && near(end(v),first))) {rotated.push_back(v);last=end(v);}
                } else if(v.verb==2 || v.verb==3) {rotated.push_back(v);last=end(v);}
            }
            if(c[found].verb==2 || c[found].verb==3)rotated.push_back(c[found]);
            rotated.push_back(command(4));
            c=std::move(rotated);
        }
        starts.erase(starts.begin()+j);
        return;
    }
}
Commands winding(const SkPath& p,bool clockwise,std::vector<SkPoint> starts) {
    std::vector<Commands> contours;
    for(const auto& c:encode(p)) {
        if(c.verb==0)contours.emplace_back();
        if(contours.empty())throw std::runtime_error("contour");
        contours.back().push_back(c);
    }
    std::stable_sort(contours.begin(),contours.end(),[](const auto& a,const auto& b){return std::abs(area(a))>std::abs(area(b));});
    std::vector<SkPath> paths;
    for(const auto& c:contours)paths.push_back(path(c));
    std::vector<size_t> nested(contours.size());
    for(size_t i=0;i<contours.size();++i)
        for(size_t j=i+1;j<contours.size();++j)
            if(inside(paths[i],contours[j],paths[j]))++nested[j];
    Commands result;
    for(size_t i=0;i<contours.size();++i) {
        auto& c=contours[i];
        if((!clockwise) ^ (area(c)<0) ^ !(nested[i]&1))c=reverse(c);
        restore(c,starts);
        result.insert(result.end(),c.begin(),c.end());
    }
    return result;
}
void firsts(const Commands& c,std::vector<SkPoint>& starts) {
    for(const auto& v:c)if(v.verb==0)starts.push_back(point(v,0));
}
Commands snap(Commands c) {
    for(auto& v:c)for(int i=0;i<points(v.verb)*2;++i)v.points[i]=std::nearbyint(v.points[i]*64)/64;
    return c;
}
}

uint32_t hd_path_version() {return 1;}
int hd_path_process(const HDCommand* input,uint64_t count,const HDOptions* options,HDResult* result) {
    if(!options || !result || (!input && count) || count>SIZE_MAX/sizeof(HDCommand))return 1;
    *result={};
    try {
        auto p=path(input,static_cast<size_t>(count));
        Commands source=encode(p),output;
        std::vector<SkPoint> starts;
        firsts(source,starts);
        if(!std::isfinite(options->radius) || options->radius<0)return 1;
        if(options->radius>0 && !p.isEmpty()) {
            SkPaint paint;
            paint.setStyle(SkPaint::kStroke_Style);
            paint.setStrokeWidth(options->radius*2);
            paint.setStrokeCap(SkPaint::kRound_Cap);
            paint.setStrokeJoin(options->pixel_grid ? SkPaint::kMiter_Join : SkPaint::kRound_Join);
            paint.setStrokeMiter(2);
            SkPathBuilder stroke;
            if(!skpathutils::FillPathWithPaint(p,paint,&stroke))return 2;
            auto strokec=encode(stroke.detach());
            auto filled=Op(p,path(strokec),kUnion_SkPathOp);
            if(!filled) {
                source=snap(source);strokec=snap(strokec);starts.clear();firsts(source,starts);
                filled=Op(path(source),path(strokec),kUnion_SkPathOp);
            }
            if(!filled)return 2;
            firsts(strokec,starts);
            output=winding(*filled,options->clockwise!=0,starts);
        } else if(options->simplify) {
            auto simple=Simplify(p);
            if(!simple)return 2;
            output=winding(*simple,options->clockwise!=0,starts);
        } else output=std::move(source);
        if(!output.empty()) {
            result->commands=static_cast<HDCommand*>(std::malloc(output.size()*sizeof(HDCommand)));
            if(!result->commands)return 3;
            std::memcpy(result->commands,output.data(),output.size()*sizeof(HDCommand));
        }
        result->count=output.size();
        return 0;
    } catch(...) {return 1;}
}
void hd_path_free(HDResult* result) {
    if(result){std::free(result->commands);*result={};}
}
