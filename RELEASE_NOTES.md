HD2 Font Maker 1.3.0-go.2 - Unicode path fix

修复中文目录、中文字体文件名和带空格路径导致图集生成失败的问题。
原始错误：atlas generation failed: Failed to load specified font file.
Go 将已处理字体原样复制到独立临时目录，向 msdf-atlas-gen 传递英文相对文件名。
保留用户原目录、设置、字体数据；不改变字体轮廓、字距、图集参数或回退优先级。

验证：
- 原始工具使用中文绝对路径可复现失败，同一字体在中文工作目录使用英文相对名成功。
- 中文/空格的程序路径、字体文件名、工作目录均由真实原生图集工具回归测试覆盖。
- PNG/RGBA、多源优先级、图集像素和字形指标与英文路径基线一致。
- 受影响的引擎/流水线/CLI测试与 MyGo GUI/backend测试、静态检查通过。
- 使用用户原设置完成真实生产backend生成，48.178秒，826639898字节，13组图集。
  五组主要字体全开，字重匹配关闭，轮廓处理开启，符号选项关闭，离线回读通过。
- 没有部署到游戏目录，没有验证游戏内显示。

运行 HD2FontMaker.exe。无需移动目录或重命名字体。
软件发行包和源码包不包含用户字体、UserData、游戏资源或生成的字体Mod。
BENCHMARK-SUMMARY.json 与 VALIDATION-1.3.0-go.1.zh-CN.txt 保留旧版基线记录。
源码构建方法见源码包 native_go/SOURCE_BUILD.txt。