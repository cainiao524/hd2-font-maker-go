"""Review only fully enumerated, ink-free contour encoding differences.

The full oracle remains unchanged. Acceptance requires exact expanded commands
after removing move/close-only contours and an explicit final line identical to
the closing edge. Zero-area contours containing drawing commands are retained.
"""
import argparse
import io
import json
from pathlib import Path
from fontTools.ttLib import TTFont
import compare_packages_fast as oracle


def normalise_encoding(commands):
    result, contour, removed = [], [], 0
    for op, points in commands:
        if op == 'moveTo':
            if contour:
                raise ValueError('unclosed contour')
            contour = [(op, points)]
        elif op == 'closePath':
            contour.append((op, points))
            if len(contour) == 2 and contour[0][0] == 'moveTo':
                removed += 1
            else:
                if (len(contour) > 3 and contour[-2][0] == 'lineTo'
                        and contour[-2][1] == contour[0][1]):
                    del contour[-2]
                result.extend(contour)
            contour = []
        else:
            contour.append((op, points))
    if contour:
        raise ValueError('open contour is not an encoding-only difference')
    return result, removed


def load_package(root):
    package = oracle.Package.__new__(oracle.Package)
    package.root = root
    package.resources, package.patches = {}, {}
    package._parse_patches()
    package.report = json.loads((root/'font-build-report.json').read_text('utf-8-sig'))
    return package


def review(path):
    report = json.loads(path.read_text('utf-8-sig'))
    work = Path(report['work_directory'])
    roots = [next(p for p in work.iterdir() if p.name.startswith(prefix))
             for prefix in ('python-', 'v13-go-')]
    packages = [load_package(p) for p in roots]
    # Python 1.3 has selections but no coverage[selected] array. The native Go
    # report has both. Validate the original fields and identical actual resource
    # sets before correcting only the old comparator's report-schema finding.
    selected, selection_fields = [], []
    for package in packages:
        if 'coverage' in package.report:
            selected.append({r['group'] for r in package.report['coverage'] if r.get('selected')})
            selection_fields.append('coverage[selected]')
        else:
            selected.append({r['group'] for r in package.report.get('selections', [])})
            selection_fields.append('selections (Python 1.3 schema)')
    required = set(report['scope']['required_groups']) if 'scope' in report else {'latin','sc','tc','jp','ko'}
    scope_schema_corrected = (selection_fields[0].startswith('selections')
                              and selected[0] == selected[1]
                              and required <= selected[0]
                              and packages[0].resources.keys() == packages[1].resources.keys())
    supported = {'glyph_geometry', 'glyph_contour_direction', 'glyph_contour_signed_area'}
    accepted_labels, rows = set(), []
    for row in report['fonts']:
        differences = row['semantic_differences']
        if not differences or any(d['kind'] not in supported for d in differences):
            continue
        if any(d['count'] != len(d['samples']) for d in differences):
            continue  # Never accept an unenumerated failure.
        cps = sorted({int(s['cp'][2:], 16) for d in differences for s in d['samples']})
        fonts = []
        for package in packages:
            resource = package.resources[tuple(row['resource'])]
            face = next(f for f in oracle.font_faces(package, resource) if f['slot'] == row['slot'])
            fonts.append(TTFont(io.BytesIO(face['data'])))
        evidence = []
        try:
            for cp in cps:
                outputs = [normalise_encoding(oracle.unrounded_commands(f, f.getBestCmap()[cp])) for f in fonts]
                evidence.append({'cp': f'U+{cp:04X}',
                                 'python_empty_move_close_contours': outputs[0][1],
                                 'go_empty_move_close_contours': outputs[1][1],
                                 'all_drawn_commands_exact_after_closing_edge_normalisation': outputs[0][0] == outputs[1][0],
                                 'has_empty_encoding_difference': outputs[0][1] != outputs[1][1]})
            accepted = all(e['all_drawn_commands_exact_after_closing_edge_normalisation']
                           and e['has_empty_encoding_difference'] for e in evidence)
            rows.append({'font': row['label'], 'accepted_as_encoding_metadata': accepted, 'characters': evidence})
            if accepted:
                accepted_labels.add(row['label'])
        finally:
            for font in fonts:
                font.close()
    unresolved = [issue for issue in report['issues'] if issue['severity'] == 'semantic'
                  and not (issue.get('font') in accepted_labels
                           and issue['category'] in {'font_'+k for k in supported})
                  and not (scope_schema_corrected and issue['category'] in
                           {'scope_missing_required', 'scope_symbol_mismatch'})]
    return {'schema': 'hd2-v13-empty-contour-review-v1',
            'oracle': str(path.resolve()), 'oracle_sha256': oracle.file_sha(path),
            'original_semantic_pass': report['summary']['semantic_pass'],
            'accepted_semantics': not unresolved,
            'scope_schema_correction': {'applied': scope_schema_corrected,
                'python_field': selection_fields[0], 'go_field': selection_fields[1],
                'python_groups': sorted(selected[0]), 'go_groups': sorted(selected[1]),
                'actual_resource_sets_equal': packages[0].resources.keys() == packages[1].resources.keys()},
            'policy': 'Only a move/close with no drawing edge may be ignored; drawn zero-area paths and reversed contours remain failures.',
            'reviewed_fonts': rows, 'unresolved_semantic_issues': unresolved}


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--oracle', required=True, type=Path)
    parser.add_argument('--out', required=True, type=Path)
    args = parser.parse_args()
    result = review(args.oracle)
    args.out.write_text(json.dumps(result, ensure_ascii=False, indent=2), encoding='utf-8')
    print(json.dumps({'accepted_semantics': result['accepted_semantics'],
                      'reviewed_fonts': len(result['reviewed_fonts']),
                      'unresolved': len(result['unresolved_semantic_issues'])}))
    raise SystemExit(0 if result['accepted_semantics'] else 1)
