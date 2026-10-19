#pragma once
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif
#define HD_HB_API __declspec(dllexport)

typedef struct { uint32_t tag; float value; } hd_hb_axis;
typedef struct { uint32_t old_gid, new_gid; } hd_hb_mapping;
typedef struct {
  uint8_t *data;
  uint64_t length;
  hd_hb_mapping *mapping;
  uint64_t mapping_count;
  char error[256];
} hd_hb_result;
typedef struct { uint32_t verb; float points[6]; } hd_hb_command;
typedef struct {
  hd_hb_command *commands;
  uint64_t count;
  char error[256];
} hd_hb_path;
typedef struct { void *handle; char error[256]; } hd_hb_face_result;
typedef struct {
  const char *text;
  uint64_t text_length;
  const char *script, *language, *direction, *features;
  uint32_t upm, reserved;
} hd_hb_shape_input;
typedef struct {
  uint32_t gid, cluster;
  int32_t x_advance, y_advance, x_offset, y_offset;
} hd_hb_glyph;
typedef struct { hd_hb_glyph *glyphs; uint64_t count; char error[256]; } hd_hb_shape_result;

HD_HB_API uint32_t hd_hb_version(void);
HD_HB_API uint32_t hd_hb_source_version(void);
HD_HB_API int hd_hb_instance(const uint8_t *, uint64_t, uint32_t,
                             const hd_hb_axis *, uint64_t, hd_hb_result *);
HD_HB_API int hd_hb_subset(const uint8_t *, uint64_t, const uint32_t *, uint64_t, hd_hb_result *);
HD_HB_API int hd_hb_remap(const uint8_t *, uint64_t, const hd_hb_mapping *, uint64_t, hd_hb_result *);
HD_HB_API void hd_hb_result_free(hd_hb_result *);
HD_HB_API int hd_hb_face_create(const uint8_t *, uint64_t, uint32_t, hd_hb_face_result *);
HD_HB_API void hd_hb_face_destroy(void *);
HD_HB_API int hd_hb_draw(void *, uint32_t, uint32_t, hd_hb_path *);
HD_HB_API void hd_hb_path_free(hd_hb_path *);
HD_HB_API int hd_hb_shape(void *, const hd_hb_shape_input *, hd_hb_shape_result *);
HD_HB_API void hd_hb_shape_free(hd_hb_shape_result *);
#ifdef __cplusplus
}
#endif
