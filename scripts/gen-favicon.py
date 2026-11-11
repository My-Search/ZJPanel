#!/usr/bin/env python3
"""生成 ZJPanel 企业级品牌图标全套资源（frontend/public/）。

设计参数集中在文件顶部，SVG 与位图由同一组参数渲染，保证视觉一致：
  深蓝渐变圆角方底 + 顶部高光 + 几何化白色「Z」字标（ZJPanel 首字母）。
仅依赖 Pillow（无需 cairosvg / ImageMagick），超采样抗锯齿后逐档 LANCZOS 缩小。

用法：python3 scripts/gen-favicon.py
"""

from pathlib import Path

from PIL import Image, ImageDraw

# ---- 设计参数（改这里即可整站换标） ----
VIEW = 64                      # 设计画布边长
RADIUS = 15                    # 圆角半径
STROKE = 1.4                   # 内描边宽度
Z_POINTS = [(19, 21), (45, 21), (19, 43), (45, 43)]  # Z 折线（顶横→斜线→底横）
Z_WIDTH = 7.5                  # Z 线宽（round cap / round join）

BG_STOPS = [                   # 底色对角渐变：#1e40af → #2563eb → #0ea5e9
    (0.00, (30, 64, 175)),
    (0.52, (37, 99, 235)),
    (1.00, (14, 165, 233)),
]
MARK_TOP = (255, 255, 255)     # Z 渐变：纯白 → 淡蓝
MARK_BOTTOM = (219, 234, 254)
GLOSS_ALPHA = 56               # 顶部高光白色最大不透明度（0.22 * 255）
INNER_STROKE_ALPHA = 72        # 内描边白色不透明度（0.28 * 255）

THEME_COLOR = "#1e40af"
BG_COLOR = "#0b1220"

SCALE = 8                      # 超采样倍数（64 * 8 = 512 绘制）
GRAD_N = 256                   # 渐变图分辨率（放大到超采样画布）
ICO_SIZES = [(16, 16), (32, 32), (48, 48)]

OUT = Path(__file__).resolve().parents[1] / "frontend" / "public"


def lerp(a: float, b: float, t: float) -> float:
    return a + (b - a) * t


def stop_color(stops, t: float):
    """按色标线性插值取色。"""
    for (t0, c0), (t1, c1) in zip(stops, stops[1:]):
        if t <= t1:
            k = (t - t0) / (t1 - t0) if t1 > t0 else 0
            return tuple(round(lerp(a, b, k)) for a, b in zip(c0, c1))
    return stops[-1][1]


def gradient_image(size: int) -> Image.Image:
    """生成对角渐变底图（低分辨率生成后平滑放大）。"""
    px = []
    for y in range(GRAD_N):
        for x in range(GRAD_N):
            t = (x + y) / (2 * (GRAD_N - 1))
            px.append(stop_color(BG_STOPS, t))
    small = Image.new("RGB", (GRAD_N, GRAD_N))
    small.putdata(px)
    return small.resize((size, size), Image.BICUBIC).convert("RGBA")


def gloss_image(size: int) -> Image.Image:
    """顶部高光：自上而下白色渐隐。"""
    row = [round(GLOSS_ALPHA * (1 - y / (GRAD_N - 1))) for y in range(GRAD_N)]
    small = Image.new("RGBA", (1, GRAD_N))
    small.putdata([(255, 255, 255, a) for a in row])
    return small.resize((size, size), Image.BICUBIC)


def mark_layer(size: int) -> Image.Image:
    """内描边 + 几何 Z 字标（白色渐变、圆角端点）。"""
    layer = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    s = size / VIEW

    box = [2 * s, 2 * s, 62 * s, 62 * s]
    d.rounded_rectangle(
        box, radius=RADIUS * s,
        outline=(255, 255, 255, INNER_STROKE_ALPHA),
        width=max(1, round(STROKE * s)),
    )

    pts = [(round(x * s), round(y * s)) for x, y in Z_POINTS]
    w = round(Z_WIDTH * s)
    # 上浅下深的竖向渐变画刷
    top, bottom = pts[0][1], pts[-1][1]
    brush = Image.new("RGBA", (size, size))
    bd = ImageDraw.Draw(brush)
    for y in range(size):
        t = max(0.0, min(1.0, (y - top) / (bottom - top))) if bottom > top else 0
        c = tuple(round(lerp(a, b, t)) for a, b in zip(MARK_TOP, MARK_BOTTOM))
        bd.line([(0, y), (size, y)], fill=(*c, 255))
    # 线条路径作遮罩，paste 到画刷上得到渐变 Z（round cap / round join）
    path = Image.new("L", (size, size), 0)
    pd = ImageDraw.Draw(path)
    pd.line(pts, fill=255, width=w, joint="curve")
    r = w / 2
    for x, y in pts:  # round cap / round join 兜底
        pd.ellipse([x - r, y - r, x + r, y + r], fill=255)
    layer.paste(brush, (0, 0), path)
    return layer


def corner_mask(size: int) -> Image.Image:
    """圆角方底遮罩。"""
    m = Image.new("L", (size, size), 0)
    ImageDraw.Draw(m).rounded_rectangle(
        [0, 0, size - 1, size - 1], radius=RADIUS * size / VIEW, fill=255
    )
    return m


def render() -> tuple[Image.Image, Image.Image]:
    """返回 (圆角透明版, 方形不透明版)。"""
    size = VIEW * SCALE
    base = gradient_image(size)
    base = Image.alpha_composite(base, gloss_image(size))
    base = Image.alpha_composite(base, mark_layer(size))

    rounded = base.copy()
    rounded.putalpha(corner_mask(size))
    # apple-touch-icon 不接受透明背景：圆角外补同色渐变
    square = Image.alpha_composite(gradient_image(size), rounded)
    return rounded, square


def svg() -> str:
    """同一组设计参数输出矢量主标。"""
    stops = "\n".join(
        f'      <stop offset="{t}" stop-color="#{r:02x}{g:02x}{b:02x}"/>'
        for t, (r, g, b) in BG_STOPS
    )
    d = "M{} {} H{} L{} {} H{}".format(
        Z_POINTS[0][0], Z_POINTS[0][1], Z_POINTS[1][0],
        Z_POINTS[2][0], Z_POINTS[2][1], Z_POINTS[3][0],
    )
    r, g, b = MARK_BOTTOM
    return f'''<svg xmlns="http://www.w3.org/2000/svg" width="{VIEW}" height="{VIEW}" viewBox="0 0 {VIEW} {VIEW}">
  <defs>
    <linearGradient id="zjp-bg" x1="0" y1="0" x2="1" y2="1">
{stops}
    </linearGradient>
    <linearGradient id="zjp-mark" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#ffffff"/>
      <stop offset="1" stop-color="#{r:02x}{g:02x}{b:02x}"/>
    </linearGradient>
    <linearGradient id="zjp-gloss" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="#ffffff" stop-opacity=".22"/>
      <stop offset="1" stop-color="#ffffff" stop-opacity="0"/>
    </linearGradient>
  </defs>
  <rect x="2" y="2" width="60" height="60" rx="{RADIUS}" fill="url(#zjp-bg)"/>
  <rect x="2" y="2" width="60" height="60" rx="{RADIUS}" fill="url(#zjp-gloss)"/>
  <rect x="2" y="2" width="60" height="60" rx="{RADIUS}" fill="none" stroke="rgba(255,255,255,.28)" stroke-width="{STROKE}"/>
  <path d="{d}" fill="none" stroke="url(#zjp-mark)" stroke-width="{Z_WIDTH}" stroke-linecap="round" stroke-linejoin="round"/>
</svg>
'''


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)
    rounded, square = render()

    (OUT / "favicon.svg").write_text(svg(), encoding="utf-8")

    pngs = {
        "favicon-16x16.png": (rounded, 16),
        "favicon-32x32.png": (rounded, 32),
        "apple-touch-icon.png": (square, 180),
        "android-chrome-192x192.png": (rounded, 192),
        "android-chrome-512x512.png": (square, 512),
    }
    for name, (src, size) in pngs.items():
        src.resize((size, size), Image.LANCZOS).save(OUT / name, "PNG")

    ico_src = square.resize((256, 256), Image.LANCZOS)
    ico_src.save(OUT / "favicon.ico", "ICO", sizes=ICO_SIZES)

    manifest = '''{
  "name": "ZJPanel",
  "short_name": "ZJPanel",
  "description": "一体化站点 · 应用 · 证书运维面板",
  "icons": [
    { "src": "/android-chrome-192x192.png", "sizes": "192x192", "type": "image/png" },
    { "src": "/android-chrome-512x512.png", "sizes": "512x512", "type": "image/png" }
  ],
  "theme_color": "%s",
  "background_color": "%s",
  "display": "standalone"
}
''' % (THEME_COLOR, BG_COLOR)
    (OUT / "site.webmanifest").write_text(manifest, encoding="utf-8")

    for p in sorted(OUT.iterdir()):
        if p.is_file():
            print(f"  {p.name:32} {p.stat().st_size:>8} B")


if __name__ == "__main__":
    main()
