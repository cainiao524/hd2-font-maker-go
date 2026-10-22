# HD2 Font Maker 1.3.0-go.3

HD2 Font Maker 的公开归档版本，包含 Go/MyGo 源码；Windows 发行包从 Releases 下载。产品以 1.3 的前端 1.3.0 / 引擎 1.3.7 行为为基线，使用 Go 引擎和 MyGo 单窗口界面，从用户自己的字体生成 Helldivers 2 字体替换包。

本版更新 MyGo 界面：分步流程、生成前缺项提示、拖放导入字体、跟随系统深色模式和应用图标，14 种界面语言同步更新。字体引擎与 1.3.0-go.2 相同，引擎修复的验证见 VALIDATION.zh-CN.txt；下文长期基线数据来自 1.3.0-go.1。

## 下载与运行

Windows x64 发行包：[GitHub Releases](/releases)

- 当前文件名：`HD2-Font-Maker-1.3.0-go.3-Windows.zip`（约 14 MB）。
- 将整个文件夹解压到游戏目录以外的可写位置。
- 运行 `HD2FontMaker.exe`。请保留 EXE 同目录的 DLL 和 `vendor` 文件夹。
- 不需要安装 Python、Go 或 WebView。

程序只读取所选游戏目录；不会启动或注入游戏，也不会自动安装生成的 Mod。

## 功能范围

- Go 引擎、MyGo 单窗口界面，提供 14 种界面语言。
- 主游戏字体选择：西文（latin）、简体中文（sc）、繁体中文（tc）、日文（jp）、韩文（ko）。
- 表情与特殊符号使用独立选项；其他游戏字体组不会因为界面语言或主字体选择而自动加入。
- 保留源字体的完整 cmap；源字体缺字时按字符回退到所选原游戏字体，支持简繁混排。
- 字重匹配与轮廓处理是两个独立开关。
- 字重处理保留字符前进宽度（advance）、kerning 和行距，不额外压缩或扩张排版单元。
- 保留目标 TTF/CFF 格式、字体单位（UPM）以及原游戏字体的家族和样式注册信息。
- 输出 RGBA MTSDF 图集及全部 mip 层级。
- 产品路径不调用 Python；Skia、HarfBuzz 和 msdf-atlas-gen 作为原生组件随包提供。

## 使用

1. 解压整个文件夹，运行 `HD2FontMaker.exe`。
2. 选择 Helldivers 2 安装目录或其 `data` 文件夹。
3. 选择或直接拖入 TTF、OTF、TTC 或 OTC 字体；字体集合可选择具体字面。
4. 选择西文、简中、繁中、日文、韩文中的目标字体组，并按需选择表情或特殊符号。
5. 选择输出 ZIP，开始生成；底部会列出生成前仍缺少的步骤。
6. 用 Mod 管理器安装生成的字体包。程序不会自行安装或启动游戏。

界面语言不会改变生成内容。字重和轮廓开关可以分别启用。

## 1.3.0-go.1 基线验证结果

最终冷生成未使用已生成结果缓存；五组主字体与符号全开，字重匹配和轮廓处理均开启。测试字体 A 用时 66.961 秒，测试字体 B 用时 114.517 秒。以上为本机单次观测，不是统计基准；不含字体导入和首次扫描游戏库存。

两套输出各包含 13 张图集，全部 mip 层级与 1.3 原实现逐字节一致；每套输出的图集 GPU 负载合计约 448 MiB，单张最大 4096×2048。

两套输出各通过 1,360 次 shaping 检查和 1,972 对 emitted geometry 检查；advance 差异为 0，cmap 缺失和多余均为 0。

完整字体审计：测试字体 A 和 B 各自完成 36 个资源槽位、17 对去重字体，分别检查 1,957,145 和 2,109,193 个 codepoint；精确空记录复核后没有未解决的语义项。

1.3 的 CFF 输出在少数已有墨迹的字形中，额外包含没有绘制指令的空 move/close 轮廓；Go 输出可能省略这些空轮廓。独立复核只接受这一编码差异，并确认在归一化闭合边后，其余全部绘制命令完全一致；有绘制边的零面积路径、轮廓方向或 signed area 变化仍按失败处理。GID、表压缩方式、字体二进制布局和 metadata 可以不同，字体文件本身并非逐字节一致。

完整验证记录见仓库根目录的 `VALIDATION.zh-CN.txt`。本版本没有进行游戏内测试，也没有部署到游戏目录。上述结果来自离线生成、回读以及独立的字体、图集和 shaping 比对，不代表每个游戏版本的实际显示效果，也不应解读为修复任何游戏内问题。

## 许可与归档范围

项目自有代码采用 MIT 许可证；第三方组件保留各自的许可证和署名。发行包和源码归档不包含个人字体、游戏原始字体、游戏资源、快照或预生成字体包。

## Build from source

The public repository preserves the `native_go` Go module at `native_go/`; `mygoapp` is a nested module and `vendor/` is at the repository root. Use Windows x64 with Go 1.27.2 or a compatible newer toolchain.

```powershell
.\native_go\mygoapp\build.ps1 -Go '<path-to-go.exe>' -Test
```

The script builds `native_go/mygoapp/bin/HD2FontMaker.exe`, runs the MyGo module tests, and copies the bundled native DLLs from `native_go/bin` and the atlas executable from the root `vendor/` directory into place. The `native_go/bin` directory and the root `vendor/` directory are supplied with the source; keep them beside the executable in a portable build. See `native_go/SOURCE_BUILD.txt` for the current build notes and the optional Skia/HarfBuzz native rebuilds. Project-owned code is MIT-licensed (see `LICENSE`); third-party notices are in `native_go/LICENSES` and `THIRD-PARTY-NOTICES.txt`.
