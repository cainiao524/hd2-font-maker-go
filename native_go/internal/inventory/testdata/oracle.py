"""Compare Go-generated synthetic fonts/archives with the original Python APIs."""
import io
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[4]))
from fonts import inspect_face, scan
from fontTools.ttLib import TTFont

request = json.load(sys.stdin)
resources = 0
if request.get("game"):
    inv = scan(Path(request["game"]))
    faces = list(inv.faces.values())
    resources = len(inv.resources)
else:
    faces = [inspect_face(bytes.fromhex(raw)) for raw in request["faces"]]
result = []
for face in faces:
    font = TTFont(io.BytesIO(face.data), lazy=True)
    result.append({"name": face.name, "group": face.group, "digest": face.digest,
                   "chars": sorted(face.chars),
                   "cmap": {chr(cp): font.getGlyphID(name) for cp, name in font.getBestCmap().items()}})
print(json.dumps({"faces": result, "resources": resources}, ensure_ascii=True))
