"""Old Python read APIs used only on Go-generated synthetic temporary files."""
import json
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).resolve().parents[4]))
from archives import Catalog, DSAR

request = json.load(sys.stdin)
catalog = Catalog(Path(request["game"]))
name = request["archive"]
_, _, entries = catalog.resources(name)
entry = entries[0]
parts = [catalog.sources[name + suffix].read(entry[2+i], entry[7+i]).hex()
         for i, suffix in enumerate(("", ".stream", ".gpu_resources"))]
dsar = DSAR(Path(request["dsar"]))
print(json.dumps(dict(zip(("Main", "Stream", "GPU", "DSAR"), parts + [dsar.read(0, dsar.size).hex()]))))
