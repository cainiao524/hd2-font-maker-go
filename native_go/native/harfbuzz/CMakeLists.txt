cmake_minimum_required(VERSION 3.20)
project(hd2harfbuzz LANGUAGES C CXX)
if(DEFINED HD_HB_INCLUDE_PREFIX)
  set(CMAKE_CL_SHOWINCLUDES_PREFIX "${HD_HB_INCLUDE_PREFIX}")
  set(CMAKE_C_CL_SHOWINCLUDES_PREFIX "${HD_HB_INCLUDE_PREFIX}")
  set(CMAKE_CXX_CL_SHOWINCLUDES_PREFIX "${HD_HB_INCLUDE_PREFIX}")
endif()
if(NOT MSVC OR NOT CMAKE_SIZEOF_VOID_P EQUAL 8)
  message(FATAL_ERROR "The hd2harfbuzz bridge requires Windows x64 MSVC")
endif()
set(CMAKE_CXX_STANDARD 17)
set(CMAKE_MSVC_RUNTIME_LIBRARY "MultiThreaded$<$<CONFIG:Debug>:Debug>")
set(BUILD_SHARED_LIBS OFF CACHE BOOL "" FORCE)
set(HB_BUILD_SUBSET ON CACHE BOOL "" FORCE)
foreach(option HB_BUILD_UTILS HB_BUILD_RASTER HB_BUILD_VECTOR HB_BUILD_GPU
    HB_HAVE_FREETYPE HB_HAVE_GLIB HB_HAVE_ICU HB_HAVE_CAIRO HB_HAVE_GRAPHITE2
    HB_HAVE_GOBJECT HB_HAVE_INTROSPECTION HB_HAVE_UNISCRIBE HB_HAVE_GDI HB_HAVE_DIRECTWRITE
    BUILD_TESTING)
  set(${option} OFF CACHE BOOL "" FORCE)
endforeach()
set(HB_BUILD_GPU_DEMO OFF CACHE STRING "" FORCE)
set(CMAKE_DISABLE_FIND_PACKAGE_Python3 ON)
add_subdirectory("${HARFBUZZ_SOURCE}" harfbuzz EXCLUDE_FROM_ALL)
# 14.6.0 only downgrades CFF2 when normalized_coords is nonempty. A static
# CFF2 has no axes to pin. Compile a patched copy, leaving pinned sources intact.
file(READ "${HARFBUZZ_SOURCE}/src/hb-subset-cff2.cc" cff2_source)
set(pin_original "pinned = instancing_requested && plan->all_axes_pinned;")
string(FIND "${cff2_source}" "${pin_original}" pin_position)
if(pin_position EQUAL -1)
  message(FATAL_ERROR "Pinned CFF2 patch no longer matches upstream")
endif()
string(REPLACE "${pin_original}"
  "pinned = (instancing_requested && plan->all_axes_pinned) ||\n             ((plan->flags & HB_SUBSET_FLAGS_DOWNGRADE_CFF2) &&\n              !hb_ot_var_get_axis_count (plan->source));"
  cff2_source "${cff2_source}")
string(REPLACE "desubroutinize = plan->flags & HB_SUBSET_FLAGS_DESUBROUTINIZE ||"
  "desubroutinize = pinned || plan->flags & HB_SUBSET_FLAGS_DESUBROUTINIZE ||"
  cff2_source "${cff2_source}")
file(CONFIGURE OUTPUT "${CMAKE_CURRENT_BINARY_DIR}/hb-subset-cff2.cc" CONTENT "${cff2_source}" @ONLY)
get_target_property(subset_sources harfbuzz-subset SOURCES)
list(FILTER subset_sources EXCLUDE REGEX "(^|[/\\\\])hb-subset-cff2\\.cc$")
set_property(TARGET harfbuzz-subset PROPERTY SOURCES "${subset_sources}")
target_sources(harfbuzz-subset PRIVATE "${CMAKE_CURRENT_BINARY_DIR}/hb-subset-cff2.cc")
# Sparse mappings can make metadata arrays much larger than tiny source tables.
# Generate the patched header and only its six direct translation-unit users so
# quoted includes resolve to this header. The pinned source tree stays intact.
set(sparse_dir "${CMAKE_CURRENT_BINARY_DIR}/sparse-subset")
file(MAKE_DIRECTORY "${sparse_dir}")
file(READ "${HARFBUZZ_SOURCE}/src/hb-subset-table.hh" subset_header)
file(READ "${CMAKE_CURRENT_SOURCE_DIR}/sparse-metadata-budget.hh" sparse_budget)
set(try_original "template<typename TableType>\nstatic bool\n_hb_subset_table_try")
set(limit_original "buf_size > c->source_blob->length * 256 ||")
set(estimate_original "return bulk + (unsigned) (table_len * sqrt ((double) dst_glyphs / src_glyphs));")
foreach(fragment "${try_original}" "${limit_original}" "${estimate_original}")
  string(FIND "${subset_header}" "${fragment}" position)
  if(position EQUAL -1)
    message(FATAL_ERROR "Pinned sparse metadata patch no longer matches upstream")
  endif()
endforeach()
string(REPLACE "${try_original}" "${sparse_budget}\n${try_original}" subset_header "${subset_header}")
string(REPLACE "${limit_original}"
  "uint64_t (buf_size) > hb_max (uint64_t (c->source_blob->length) * 256u,\n                                uint64_t (_hd2_sparse_metadata_budget (c->plan, c->table_tag))) ||"
  subset_header "${subset_header}")
string(REPLACE "${estimate_original}"
  "return hb_max (bulk + (unsigned) (table_len * sqrt ((double) dst_glyphs / src_glyphs)),\n                 _hd2_sparse_metadata_budget (plan, table_tag));"
  subset_header "${subset_header}")
file(CONFIGURE OUTPUT "${sparse_dir}/hb-subset-table.hh" CONTENT "${subset_header}" @ONLY)
get_target_property(subset_sources harfbuzz-subset SOURCES)
foreach(unit hb-subset hb-subset-table-cff hb-subset-table-layout
    hb-subset-table-color hb-subset-table-var hb-subset-table-other)
  file(READ "${HARFBUZZ_SOURCE}/src/${unit}.cc" unit_source)
  file(CONFIGURE OUTPUT "${sparse_dir}/${unit}.cc" CONTENT "${unit_source}" @ONLY)
  list(FILTER subset_sources EXCLUDE REGEX "(^|[/\\\\])${unit}\\.cc$")
  list(APPEND subset_sources "${sparse_dir}/${unit}.cc")
endforeach()
set_property(TARGET harfbuzz-subset PROPERTY SOURCES "${subset_sources}")
add_library(hd2harfbuzz SHARED bridge.cpp)
target_link_libraries(hd2harfbuzz PRIVATE harfbuzz-subset harfbuzz)
target_compile_options(hd2harfbuzz PRIVATE /W4 /EHsc /utf-8)
target_compile_definitions(hd2harfbuzz PRIVATE _CRT_SECURE_NO_WARNINGS)
