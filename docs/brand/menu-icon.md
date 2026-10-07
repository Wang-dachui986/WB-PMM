# 菜单栏图标

- 用途：替换菜单栏原圆形状态符号；28 × 16 pt，macOS 模板渲染自动适应深浅背景。
- 素材：`macos/WorkBuddyDesktop/Assets/WBPMMMenuTemplate.png`，透明 PNG。
- 来源：用户本轮提供的白色 JPG 标志；此前提供的同形蓝色标志用于辨认轮廓。
- 处理方式：内置 imagegen 提取透明单色图，未使用 API/CLI 回退。原始附件未修改。
- 服务状态保留在菜单文字、悬停提示和辅助功能标签中。

## 本次处理提示词

```text
Extract the existing W3 / WB rounded flowing monogram from these two references as a macOS menu bar template icon. Reference 1 is the user's desired white version but nearly white on white JPEG; reference 2 shows the exact same existing mark in blue and is provided ONLY for clear geometry. Preserve that exact silhouette, proportions, joined strokes, rounded ends and open counters. Do NOT redesign it or add any strokes/letters/symbols. Remove the entire background and all white padding. Output the monogram as solid BLACK opaque silhouette with smooth antialiased edges on truly TRANSPARENT alpha background, no shadows, no gradients, no texture, no border, no rounded square tile, no checkerboard drawn into the pixels. Tight horizontal composition about 1.77:1 aspect ratio, only a tiny transparent margin around the shape so it reads at 24x16 screen points. This is technical background extraction and monochrome preparation of the supplied logo, not logo generation.
```
