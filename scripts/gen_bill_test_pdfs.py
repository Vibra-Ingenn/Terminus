"""Turn the test bills into PDFs four ways, for Terminus tests.

Run: uvx --with pypdf --with cryptography --with reportlab python -I scripts/gen_bill_test_pdfs.py <bills folder>

For each bill: plain, nospace (no space characters, every word placed on its own, the way
accounting programs write PDFs), form (all text inside a Form XObject) and locked (RC4 40-bit,
opens without a password). Plus two receipts with their total line removed.
"""
import sys
from io import BytesIO
from pathlib import Path

from pypdf import PdfReader, PdfWriter
from reportlab.lib.pagesizes import letter
from reportlab.pdfbase.pdfmetrics import stringWidth
from reportlab.pdfgen import canvas

SRC = Path(sys.argv[1])
OUT = Path(__file__).resolve().parents[1] / "internal" / "tools" / "testdata" / "bills"
OUT.mkdir(parents=True, exist_ok=True)
FONT, SIZE = "Helvetica", 11


def render(lines, mode):
    buf = BytesIO()
    c = canvas.Canvas(buf, pagesize=letter)
    if mode == "form":
        c.beginForm("body")
    y = 720
    for line in lines:
        if mode == "nospace":
            x = 72
            for word in line.split(" "):
                if word:
                    c.setFont(FONT, SIZE)
                    c.drawString(x, y, word)
                x += stringWidth(word + " ", FONT, SIZE)
        else:
            c.setFont(FONT, SIZE)
            c.drawString(72, y, line)
        y -= 18
    if mode == "form":
        c.endForm()
        c.doForm("body")
    c.showPage()
    c.save()
    return buf.getvalue()


def lock(data):
    w = PdfWriter(clone_from=PdfReader(BytesIO(data)))
    w.encrypt(user_password="", owner_password="owner-pass", algorithm="RC4-40")
    out = BytesIO()
    w.write(out)
    return out.getvalue()


bills = ["acme_supply_INV-2041", "brightline_electric_2026-09", "cloudpos_subscription_oct", "greenleaf_produce_8812"]
for name in bills:
    lines = (SRC / f"{name}.txt").read_text(encoding="utf-8").splitlines()
    for mode in ("plain", "nospace", "form"):
        (OUT / f"{name}_{mode}.pdf").write_bytes(render(lines, mode))
    (OUT / f"{name}_locked.pdf").write_bytes(lock(render(lines, "plain")))

for name in ("acme_supply_INV-2041", "cloudpos_subscription_oct"):
    lines = [l for l in (SRC / f"{name}.txt").read_text(encoding="utf-8").splitlines() if "total" not in l.lower()]
    (OUT / f"{name}_nototal.pdf").write_bytes(render(lines, "plain"))
print("wrote", len(list(OUT.glob("*.pdf"))), "PDFs to", OUT)
