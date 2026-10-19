"""Development oracle. FontTools is not part of the native product."""
import base64
import io
import json
from pathlib import Path

from fontTools.fontBuilder import FontBuilder
from fontTools.pens.ttGlyphPen import TTGlyphPen
from fontTools.feaLib.builder import addOpenTypeFeaturesFromString
from fontTools.ttLib import TTFont
from fontTools.ttLib.scaleUpem import scale_upem

ROOT=Path(__file__).resolve().parent
ORDER=['.notdef','A','V','Alig','B','W','Blig','acuteA','acuteB','Aalt','Balt']


def make(features):
    builder=FontBuilder(1000,isTTF=True)
    builder.setupGlyphOrder(ORDER)
    builder.setupCharacterMap({65:'A',86:'V',66:'B',87:'W',0x301:'acuteA',0x302:'acuteB'})
    glyphs={}
    for name in ORDER:
        pen=TTGlyphPen(None)
        if name!='.notdef':
            pen.moveTo((20,0));pen.lineTo((550,0));pen.lineTo((550,700));pen.lineTo((20,700));pen.closePath()
        glyphs[name]=pen.glyph()
    builder.setupGlyf(glyphs)
    builder.setupHorizontalMetrics({name:(0 if name.startswith('acute') else 600,20) for name in ORDER})
    builder.setupHorizontalHeader(ascent=800,descent=-200)
    builder.setupOS2(sTypoAscender=800,sTypoDescender=-200,usWinAscent=800,usWinDescent=200)
    builder.setupNameTable({'familyName':'Synthetic','styleName':'Regular','uniqueFontIdentifier':'Synthetic','fullName':'Synthetic','psName':'Synthetic'})
    builder.setupPost();builder.setupMaxp()
    addOpenTypeFeaturesFromString(builder.font,features)
    return builder.font


def rules(prefix,second,amount):
    return f'''
        @marks{prefix}=[acute{prefix}];
        markClass acute{prefix} <anchor 30 60> @top{prefix};
        lookup single{prefix} {{ sub {prefix} by {prefix}alt; }} single{prefix};
        feature liga {{ sub {prefix} {second} by {prefix}lig; }} liga;
        feature calt {{ sub {prefix}' lookup single{prefix} {second}; }} calt;
        feature kern {{ pos {prefix} {second} {amount}; }} kern;
        feature mark {{ lookupflag UseMarkFilteringSet @marks{prefix}; pos base {prefix} <anchor 300 700> mark @top{prefix}; }} mark;
        feature mkmk {{ lookupflag MarkAttachmentType @marks{prefix}; pos mark acute{prefix} <anchor 10 180> mark @top{prefix}; }} mkmk;
        table GDEF {{ LigatureCaretByPos {prefix}lig 300; }} GDEF;
    '''


def main():
    a=make('languagesystem DFLT dflt;'+rules('A','V',-80))
    b=make('languagesystem DFLT dflt;'+rules('B','W',-120))
    a.save(ROOT/'base.ttf');b.save(ROOT/'donor.ttf')
    cases=[]
    for label,font in (('base',a),('donor',b)):
        raw=io.BytesIO();font.save(raw)
        scaled=TTFont(io.BytesIO(raw.getvalue()),recalcTimestamp=False)
        scale_upem(scaled,512)
        out=io.BytesIO();scaled.save(out)
        scaled=TTFont(io.BytesIO(out.getvalue()))
        font=TTFont(io.BytesIO(raw.getvalue()))
        for tag in ('GPOS','GDEF'):
            cases.append({'name':label+'-'+tag,'tag':tag,'ratio':.512,'input':base64.b64encode(font.reader[tag]).decode(),'scaled':base64.b64encode(scaled.reader[tag]).decode()})
    (ROOT/'scale.json').write_text(json.dumps(cases,indent=2)+'\n')


if __name__=='__main__':main()
