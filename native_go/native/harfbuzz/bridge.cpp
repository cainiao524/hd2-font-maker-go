#include "bridge.h"
#include <hb.h>
#include <hb-ot.h>
#include <hb-subset.h>
#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <memory>
#include <stdexcept>
#include <string>
#include <vector>

namespace {
template<class T, void (*Destroy)(T*)>
using owned = std::unique_ptr<T, decltype(Destroy)>;
using face_ptr = owned<hb_face_t, hb_face_destroy>;
using blob_ptr = owned<hb_blob_t, hb_blob_destroy>;
using input_ptr = owned<hb_subset_input_t, hb_subset_input_destroy>;
using plan_ptr = owned<hb_subset_plan_t, hb_subset_plan_destroy>;
using font_ptr = owned<hb_font_t, hb_font_destroy>;
using draw_ptr = owned<hb_draw_funcs_t, hb_draw_funcs_destroy>;
using buffer_ptr = owned<hb_buffer_t, hb_buffer_destroy>;

void require(bool condition, const char *message) {
  if (!condition) throw std::runtime_error(message);
}
template<class F> int guarded(char *error, F fn) noexcept {
  try { fn(); return 0; }
  catch (const std::exception &e) { std::snprintf(error, 256, "%s", e.what()); }
  catch (...) { std::snprintf(error, 256, "unknown native HarfBuzz failure"); }
  return 1;
}
face_ptr make_face(const uint8_t *data, uint64_t length, uint32_t index) {
  require(data && length && length <= UINT32_MAX, "invalid font data size");
  blob_ptr blob(hb_blob_create_or_fail(reinterpret_cast<const char *>(data),
      static_cast<unsigned>(length), HB_MEMORY_MODE_DUPLICATE, nullptr, nullptr), hb_blob_destroy);
  require(bool(blob), "font allocation failed");
  require(index < hb_face_count(blob.get()), "invalid font face index or container");
  face_ptr face(hb_face_create(blob.get(), index), hb_face_destroy);
  require(face.get() != hb_face_get_empty() && hb_face_get_glyph_count(face.get()) > 0,
          "invalid font face or empty glyph set");
  hb_face_make_immutable(face.get());
  return face;
}
struct face_state {
  face_ptr face;
  font_ptr font;
  explicit face_state(face_ptr source) : face(std::move(source)), font(hb_font_create(face.get()), hb_font_destroy) {
    require(font.get() != hb_font_get_empty(), "font allocation failed");
    hb_ot_font_set_funcs(font.get());
    hb_font_make_immutable(font.get());
  }
  font_ptr scaled_font(uint32_t upm) const {
    require(upm <= 65535, "output UPM outside SFNT range");
    if (!upm || upm == hb_face_get_upem(face.get()))
      return font_ptr(hb_font_reference(font.get()), hb_font_destroy);
    font_ptr scaled(hb_font_create(face.get()), hb_font_destroy);
    require(scaled.get() != hb_font_get_empty(), "font allocation failed");
    hb_ot_font_set_funcs(scaled.get());
    hb_font_set_scale(scaled.get(), static_cast<int>(upm), static_cast<int>(upm));
    return scaled;
  }
};
input_ptr make_input(bool everything) {
  input_ptr input(hb_subset_input_create_or_fail(), hb_subset_input_destroy);
  require(bool(input), "subset input allocation failed");
  if (everything) hb_subset_input_keep_everything(input.get());
  else {
    for (auto kind : {HB_SUBSET_SETS_LAYOUT_FEATURE_TAG, HB_SUBSET_SETS_LAYOUT_SCRIPT_TAG,
                     HB_SUBSET_SETS_NAME_ID, HB_SUBSET_SETS_NAME_LANG_ID}) {
      auto *set = hb_subset_input_set(input.get(), kind);
      hb_set_clear(set);
      hb_set_invert(set);
    }
    hb_subset_input_set_flags(input.get(), static_cast<hb_subset_flags_t>(
        HB_SUBSET_FLAGS_NOTDEF_OUTLINE | HB_SUBSET_FLAGS_GLYPH_NAMES | HB_SUBSET_FLAGS_NAME_LEGACY));
  }
  return input;
}
plan_ptr make_plan(hb_face_t *face, hb_subset_input_t *input) {
  plan_ptr plan(hb_subset_plan_create_or_fail(face, input), hb_subset_plan_destroy);
  require(bool(plan), "HarfBuzz subset planning failed");
  return plan;
}
std::vector<hd_hb_mapping> read_mapping(hb_subset_plan_t *plan) {
  std::vector<hd_hb_mapping> result;
  auto *map = hb_subset_plan_old_to_new_glyph_mapping(plan);
  result.reserve(hb_map_get_population(map));
  int i = -1;
  uint32_t old_gid, new_gid;
  while (hb_map_next(map, &i, &old_gid, &new_gid)) result.push_back({old_gid, new_gid});
  std::sort(result.begin(), result.end(), [](auto a, auto b) { return a.old_gid < b.old_gid; });
  return result;
}
void execute(hb_subset_plan_t *plan, hd_hb_result *out, bool mapping) {
  face_ptr face(hb_subset_plan_execute_or_fail(plan), hb_face_destroy);
  require(bool(face), "HarfBuzz subset execution failed");
  blob_ptr blob(hb_face_reference_blob(face.get()), hb_blob_destroy);
  unsigned length = 0;
  const char *data = hb_blob_get_data(blob.get(), &length);
  require(data && length, "HarfBuzz produced an empty font");
  out->data = static_cast<uint8_t *>(std::malloc(length));
  require(out->data, "font result allocation failed");
  out->length = length;
  std::memcpy(out->data, data, length);
  if (mapping) {
    auto entries = read_mapping(plan);
    out->mapping = static_cast<hd_hb_mapping *>(std::malloc(entries.size() * sizeof(hd_hb_mapping)));
    require(out->mapping, "mapping result allocation failed");
    out->mapping_count = entries.size();
    std::memcpy(out->mapping, entries.data(), entries.size() * sizeof(hd_hb_mapping));
  }
}
struct drawing {
  std::vector<hd_hb_command> commands;
  bool failed = false;
  void add(hd_hb_command command) noexcept {
    if (failed) return;
    try { commands.push_back(command); } catch (...) { failed = true; }
  }
};
void move(hb_draw_funcs_t *, void *data, hb_draw_state_t *, float x, float y, void *) {
  static_cast<drawing *>(data)->add({0, {x, y}});
}
void line(hb_draw_funcs_t *, void *data, hb_draw_state_t *, float x, float y, void *) {
  static_cast<drawing *>(data)->add({1, {x, y}});
}
void quad(hb_draw_funcs_t *, void *data, hb_draw_state_t *, float x1, float y1, float x2, float y2, void *) {
  static_cast<drawing *>(data)->add({2, {x1, y1, x2, y2}});
}
void cubic(hb_draw_funcs_t *, void *data, hb_draw_state_t *, float x1, float y1,
           float x2, float y2, float x3, float y3, void *) {
  static_cast<drawing *>(data)->add({3, {x1, y1, x2, y2, x3, y3}});
}
void close(hb_draw_funcs_t *, void *data, hb_draw_state_t *, void *) {
  static_cast<drawing *>(data)->add({4, {}});
}
hb_draw_funcs_t *draw_functions() {
  static draw_ptr funcs([] {
    auto *f = hb_draw_funcs_create();
    hb_draw_funcs_set_move_to_func(f, move, nullptr, nullptr);
    hb_draw_funcs_set_line_to_func(f, line, nullptr, nullptr);
    hb_draw_funcs_set_quadratic_to_func(f, quad, nullptr, nullptr);
    hb_draw_funcs_set_cubic_to_func(f, cubic, nullptr, nullptr);
    hb_draw_funcs_set_close_path_func(f, close, nullptr, nullptr);
    hb_draw_funcs_make_immutable(f);
    return f;
  }(), hb_draw_funcs_destroy);
  return funcs.get();
}
} // namespace

static_assert(sizeof(hd_hb_command) == 28, "geometry ABI");
static_assert(sizeof(hd_hb_mapping) == 8 && sizeof(hd_hb_axis) == 8, "input ABI");
static_assert(sizeof(hd_hb_result) == 288 && sizeof(hd_hb_path) == 272, "result ABI");

uint32_t hd_hb_version(void) { return 1; }
uint32_t hd_hb_source_version(void) { return HB_VERSION_MAJOR * 10000 + HB_VERSION_MINOR * 100 + HB_VERSION_MICRO; }
void hd_hb_result_free(hd_hb_result *out) {
  if (!out) return;
  std::free(out->data);
  std::free(out->mapping);
  *out = {};
}
int hd_hb_instance(const uint8_t *data, uint64_t length, uint32_t index,
                   const hd_hb_axis *axes, uint64_t count, hd_hb_result *out) {
  if (!out) return 1;
  *out = {};
  return guarded(out->error, [&] {
    require(axes || !count, "missing axis values");
    auto face = make_face(data, length, index);
    blob_ptr source_cff2(hb_face_reference_table(face.get(), HB_TAG('C','F','F','2')), hb_blob_destroy);
    auto input = make_input(true);
    if (hb_ot_var_get_axis_count(face.get()))
      require(hb_subset_input_pin_all_axes_to_default(input.get(), face.get()), "cannot pin default axes");
    for (uint64_t i = 0; i < count; ++i) {
      require(std::isfinite(axes[i].value), "axis value must be finite");
      require(hb_subset_input_pin_axis_location(input.get(), face.get(), axes[i].tag, axes[i].value),
              "unknown axis or axis pinning failed");
    }
    hb_subset_input_set_flags(input.get(), static_cast<hb_subset_flags_t>(
        hb_subset_input_get_flags(input.get()) | HB_SUBSET_FLAGS_DOWNGRADE_CFF2));
    auto plan = make_plan(face.get(), input.get());
    execute(plan.get(), out, false);
    auto result = make_face(out->data, out->length, 0);
    blob_ptr fvar(hb_face_reference_table(result.get(), HB_TAG('f','v','a','r')), hb_blob_destroy);
    blob_ptr cff2(hb_face_reference_table(result.get(), HB_TAG('C','F','F','2')), hb_blob_destroy);
    blob_ptr cff1(hb_face_reference_table(result.get(), HB_TAG('C','F','F',' ')), hb_blob_destroy);
    require(!hb_blob_get_length(fvar.get()), "HarfBuzz did not fully pin all variation axes");
    require(!hb_blob_get_length(cff2.get()), "HarfBuzz did not convert CFF2 to CFF1");
    require(!hb_blob_get_length(source_cff2.get()) || hb_blob_get_length(cff1.get()), "CFF2 conversion lost CFF1 outlines");
  });
}
int hd_hb_subset(const uint8_t *data, uint64_t length, const uint32_t *codepoints,
                 uint64_t count, hd_hb_result *out) {
  if (!out) return 1;
  *out = {};
  return guarded(out->error, [&] {
    require(codepoints || !count, "missing codepoints");
    auto face = make_face(data, length, 0);
    auto input = make_input(false);
    for (uint32_t gid = 1; gid < 3 && gid < hb_face_get_glyph_count(face.get()); ++gid)
      hb_set_add(hb_subset_input_glyph_set(input.get()), gid);
    auto *unicodes = hb_subset_input_unicode_set(input.get());
    for (uint64_t i = 0; i < count; ++i) {
      auto cp = codepoints[i];
      // Source cmap keys are opaque encoded codes, not text scalar values.
      require(cp <= 0x10ffff, "source cmap code outside 0..0x10ffff");
      hb_set_add(unicodes, cp);
    }
    require(hb_set_allocation_successful(unicodes), "Unicode set allocation failed");
    auto plan = make_plan(face.get(), input.get());
    execute(plan.get(), out, false);
    // Explicitly retained compatibility glyphs must not expand Unicode coverage.
    // Return only requested codepoints and their final IDs for the Go cmap writer.
    std::vector<hd_hb_mapping> unicode_map;
    auto *unicode_to_old = hb_subset_plan_unicode_to_old_glyph_mapping(plan.get());
    auto *old_to_new = hb_subset_plan_old_to_new_glyph_mapping(plan.get());
    for (uint64_t i = 0; i < count; ++i) {
      auto old_gid = hb_map_get(unicode_to_old, codepoints[i]);
      auto new_gid = hb_map_get(old_to_new, old_gid);
      if (new_gid != HB_MAP_VALUE_INVALID) unicode_map.push_back({codepoints[i], new_gid});
    }
    if (!unicode_map.empty()) {
      out->mapping = static_cast<hd_hb_mapping *>(std::malloc(unicode_map.size() * sizeof(hd_hb_mapping)));
      require(out->mapping, "Unicode mapping allocation failed");
      out->mapping_count = unicode_map.size();
      std::memcpy(out->mapping, unicode_map.data(), unicode_map.size() * sizeof(hd_hb_mapping));
    }
  });
}
int hd_hb_remap(const uint8_t *data, uint64_t length, const hd_hb_mapping *requested,
                uint64_t count, hd_hb_result *out) {
  if (!out) return 1;
  *out = {};
  return guarded(out->error, [&] {
    require(requested || !count, "missing glyph mapping");
    auto face = make_face(data, length, 0);
    auto input = make_input(false);
    auto *glyphs = hb_subset_input_glyph_set(input.get());
    auto *map = hb_subset_input_old_to_new_glyph_mapping(input.get());
    const auto num_glyphs = hb_face_get_glyph_count(face.get());
    std::vector<bool> used(65535, false), source_used(num_glyphs, false);
    used[0] = true;
    hb_set_add(glyphs, 0);
    hb_map_set(map, 0, 0);
    for (uint64_t i = 0; i < count; ++i) {
      auto entry = requested[i];
      require(entry.old_gid < num_glyphs && entry.new_gid < 65535, "glyph mapping outside SFNT range");
      require(!source_used[entry.old_gid], "duplicate source glyph ID");
      source_used[entry.old_gid] = true;
      require(entry.old_gid != 0 || entry.new_gid == 0, ".notdef must map to glyph zero");
      require(entry.old_gid == 0 || !used[entry.new_gid], "duplicate destination glyph ID or reserved glyph zero");
      used[entry.new_gid] = true;
      hb_map_set(map, entry.old_gid, entry.new_gid);
      hb_set_add(glyphs, entry.old_gid);
    }
    require(hb_map_allocation_successful(map) && hb_set_allocation_successful(glyphs), "mapping allocation failed");
    // Plan first: closure adds GSUB, component, color and other referenced glyphs.
    // HarfBuzz allocates their IDs above the largest requested destination.
    auto plan = make_plan(face.get(), input.get());
    auto actual = read_mapping(plan.get());
    uint32_t previous = 0;
    for (auto entry : actual) {
      require(entry.new_gid < 65535, "closure exceeds SFNT glyph capacity");
      require(entry.old_gid == 0 || entry.new_gid > previous,
              "unsupported non-monotonic mapping: HarfBuzz may emit unsorted layout Coverage tables");
      previous = entry.new_gid;
    }
    auto *actual_map = hb_subset_plan_old_to_new_glyph_mapping(plan.get());
    for (uint64_t i = 0; i < count; ++i)
      require(hb_map_get(actual_map, requested[i].old_gid) == requested[i].new_gid,
              "HarfBuzz did not honor the requested glyph mapping");
    execute(plan.get(), out, true);
  });
}
int hd_hb_face_create(const uint8_t *data, uint64_t length, uint32_t index, hd_hb_face_result *out) {
  if (!out) return 1;
  *out = {};
  return guarded(out->error, [&] { out->handle = new face_state(make_face(data, length, index)); });
}
void hd_hb_face_destroy(void *face) { delete static_cast<face_state *>(face); }
int hd_hb_draw(void *handle, uint32_t gid, uint32_t upm, hd_hb_path *out) {
  if (!out) return 1;
  *out = {};
  return guarded(out->error, [&] {
    auto *face = static_cast<face_state *>(handle);
    require(face && gid < hb_face_get_glyph_count(face->face.get()), "glyph ID outside font");
    auto font = face->scaled_font(upm);
    drawing path;
    require(hb_font_draw_glyph_or_fail(font.get(), gid, draw_functions(), &path), "cannot draw glyph outline");
    require(!path.failed, "drawing allocation failed");
    if (path.commands.empty()) return;
    out->commands = static_cast<hd_hb_command *>(std::malloc(path.commands.size() * sizeof(hd_hb_command)));
    require(out->commands, "path result allocation failed");
    out->count = path.commands.size();
    std::memcpy(out->commands, path.commands.data(), path.commands.size() * sizeof(hd_hb_command));
  });
}
void hd_hb_path_free(hd_hb_path *out) {
  if (!out) return;
  std::free(out->commands);
  *out = {};
}
int hd_hb_shape(void *handle, const hd_hb_shape_input *input, hd_hb_shape_result *out) {
  if (!out) return 1;
  *out = {};
  return guarded(out->error, [&] {
    auto *face = static_cast<face_state *>(handle);
    require(face && input && (input->text || !input->text_length) && input->text_length <= INT32_MAX,
            "invalid shape input");
    auto font = face->scaled_font(input->upm);
    buffer_ptr buffer(hb_buffer_create(), hb_buffer_destroy);
    require(buffer.get() != hb_buffer_get_empty(), "shape buffer allocation failed");
    hb_buffer_add_utf8(buffer.get(), input->text ? input->text : "", static_cast<int>(input->text_length),
                       0, static_cast<int>(input->text_length));
    if (input->script && *input->script) {
      auto script = hb_script_from_string(input->script, -1);
      require(script != HB_SCRIPT_INVALID, "invalid shaping script");
      hb_buffer_set_script(buffer.get(), script);
    }
    if (input->language && *input->language)
      hb_buffer_set_language(buffer.get(), hb_language_from_string(input->language, -1));
    if (input->direction && *input->direction) {
      auto direction = hb_direction_from_string(input->direction, -1);
      require(direction != HB_DIRECTION_INVALID, "invalid shaping direction");
      hb_buffer_set_direction(buffer.get(), direction);
    }
    hb_buffer_guess_segment_properties(buffer.get());
    std::vector<hb_feature_t> features;
    std::string specification(input->features ? input->features : "");
    size_t start = 0;
    while (start < specification.size()) {
      size_t end = specification.find(',', start);
      if (end == std::string::npos) end = specification.size();
      hb_feature_t feature;
      require(hb_feature_from_string(specification.data() + start, static_cast<int>(end - start), &feature),
              "invalid shaping feature");
      features.push_back(feature);
      start = end + 1;
    }
    require(hb_shape_full(font.get(), buffer.get(), features.data(), static_cast<unsigned>(features.size()), nullptr),
            "HarfBuzz shaping failed");
    require(hb_buffer_allocation_successful(buffer.get()), "shape buffer allocation failed");
    unsigned count = 0;
    const auto *info = hb_buffer_get_glyph_infos(buffer.get(), &count);
    const auto *positions = hb_buffer_get_glyph_positions(buffer.get(), nullptr);
    if (!count) return;
    out->glyphs = static_cast<hd_hb_glyph *>(std::malloc(count * sizeof(hd_hb_glyph)));
    require(out->glyphs, "shaping result allocation failed");
    out->count = count;
    for (unsigned i = 0; i < count; ++i)
      out->glyphs[i] = {info[i].codepoint, info[i].cluster, positions[i].x_advance, positions[i].y_advance,
                        positions[i].x_offset, positions[i].y_offset};
  });
}
void hd_hb_shape_free(hd_hb_shape_result *out) {
  if (!out) return;
  std::free(out->glyphs);
  *out = {};
}
