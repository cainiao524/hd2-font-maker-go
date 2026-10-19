"""Development-only cu2qu oracle; product code is native Go."""
import json
from pathlib import Path
import random
from fontTools.cu2qu import curve_to_quadratic

random.seed(1729)
rows=[]
curves=[[(0,0),(100,400),(200,-100),(400,0)],[(0,0)]*4,
        [(0,0),(100,100),(200,100),(300,0)],[(0,0),(0,0),(100,100),(100,100)]]
curves += [[(random.randrange(-2000,2000),random.randrange(-2000,2000)) for _ in range(4)] for _ in range(250)]
for curve in curves:
    for tolerance in (1.0,2.048):
        rows.append({'curve':curve,'tolerance':tolerance,'spline':curve_to_quadratic(curve,tolerance)})
Path(__file__).with_name('oracle.json').write_text(json.dumps(rows)+'\n')
