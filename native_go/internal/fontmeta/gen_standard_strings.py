"""Development-only generation from the installed official FontTools package."""
import json
from pathlib import Path
from fontTools.cffLib import cffStandardStrings

assert len(cffStandardStrings) == 391
root = Path(__file__).resolve().parent
source = """// Code generated from FontTools cffStandardStrings; DO NOT EDIT.
// Source: https://github.com/fonttools/fonttools/blob/4.67.0/Lib/fontTools/cffLib/__init__.py
// FontTools is MIT-licensed; see FontTools-LICENSE.txt.
package fontmeta

var standardStrings = [...]string{
"""
source += "".join("\t" + json.dumps(value, ensure_ascii=True) + ",\n" for value in cffStandardStrings)
source += "}\n"
(root / "standard_strings.go").write_text(source, encoding="ascii")
