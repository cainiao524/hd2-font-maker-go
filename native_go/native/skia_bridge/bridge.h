#pragma once
#include <cstdint>

struct HDCommand { uint32_t verb; float points[6]; };
struct HDOptions { float radius; uint32_t pixel_grid; uint32_t clockwise; uint32_t simplify; };
struct HDResult { HDCommand* commands; uint64_t count; };

extern "C" __declspec(dllexport) uint32_t hd_path_version();
extern "C" __declspec(dllexport) int hd_path_process(const HDCommand*, uint64_t, const HDOptions*, HDResult*);
extern "C" __declspec(dllexport) void hd_path_free(HDResult*);
