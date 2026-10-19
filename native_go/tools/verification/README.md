# Optional offline package verification

These Python tools are development checks. The Go application does not import
or start them. Install `requirements.txt` only when running the checks.

Use an independently generated Python 1.3 reference ZIP and the Go ZIP from
the same font and options. No game assets or private font packages are supplied.
Name the inputs `python-example.zip` and `v13-go-example.zip` so the extracted
packages can also be located by the follow-up tools.

```text
python compare_packages_fast.py --python python-example.zip --go v13-go-example.zip --out comparison.json --keep-work
python review_empty_contours.py --oracle comparison.json --out encoding-review.json
python compare_shaping.py --work <work_directory from comparison.json> --out shaping.json
```

The original comparison report is kept unchanged. The follow-up review checks
Python 1.3's `selections` report schema against the actual resource identities,
and accepts only fully enumerated empty move/close records when all remaining
draw commands are exactly equal. Drawn zero-area contours, direction reversals,
coverage changes, or metrics changes are not waived. Read both reports.

The fast comparator checks every codepoint. It skips redundant area calculations
only when the unrounded, fully expanded drawing commands are exactly equal;
all other glyphs use the complete geometry and direction checks.
