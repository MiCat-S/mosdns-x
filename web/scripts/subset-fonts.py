#!/usr/bin/env python3
"""Subset the Chinese and Japanese title serifs to the panel's own text.

Titles and figures are set in Noto Serif SC / JP (SIL OFL 1.1). The full
fonts are 10+ MB per weight, so the panel ships only the characters its
interface text uses: every CJK character in web/src (sources and locale
tables, tests excluded) plus a few date characters Intl emits. Run this
again whenever i18n.test.ts reports characters missing from the subset.

Usage:
  python3 -m venv /tmp/fonts && /tmp/fonts/bin/pip install fonttools brotli
  # Noto Serif SC/JP Medium and SemiBold OTFs from
  # https://github.com/notofonts/noto-cjk/tree/main/Serif/SubsetOTF
  /tmp/fonts/bin/python web/scripts/subset-fonts.py <dir-with-otf-files>
"""

import re
import sys
from pathlib import Path

from fontTools import subset

WEB = Path(__file__).resolve().parent.parent
SRC = WEB / "src"
OUT = SRC / "fonts"

# Characters Intl and the page add around the interface strings.
EXTRA = "年月日时分秒周时間曜午前後〇一二三四五六七八九十"
CJK = re.compile(r"[　-〿぀-ヿ㐀-䶿一-鿿＀-￯‘-‟…·]")

FONTS = {
    "serif-sc-500": "NotoSerifSC-Medium.otf",
    "serif-sc-600": "NotoSerifSC-SemiBold.otf",
    "serif-jp-500": "NotoSerifJP-Medium.otf",
    "serif-jp-600": "NotoSerifJP-SemiBold.otf",
}


def charset() -> str:
    chars = set(EXTRA)
    for path in list(SRC.glob("*.ts")) + list(SRC.glob("*.tsx")) + list(
        (SRC / "locales").glob("*.ts")
    ):
        if ".test." in path.name:
            continue
        chars.update(CJK.findall(path.read_text(encoding="utf-8")))
    return "".join(sorted(chars))


def main() -> None:
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    source = Path(sys.argv[1])
    text = charset()
    OUT.mkdir(exist_ok=True)
    (OUT / "charset.txt").write_text(text + "\n", encoding="utf-8")
    for name, file in FONTS.items():
        options = subset.Options()
        options.flavor = "woff2"
        options.layout_features = ["*"]
        options.name_IDs = ["*"]
        options.notdef_outline = True
        font = subset.load_font(str(source / file), options)
        subsetter = subset.Subsetter(options)
        subsetter.populate(text=text)
        subsetter.subset(font)
        target = OUT / f"{name}.woff2"
        subset.save_font(font, str(target), options)
        print(f"{target.relative_to(WEB)}: {target.stat().st_size // 1024} KB")
    print(f"{len(text)} characters")


if __name__ == "__main__":
    main()
