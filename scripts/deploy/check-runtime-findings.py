"""Fail on actionable high/critical findings; retain the complete scan as evidence."""
import json, pathlib, sys

def evaluate(report):
    if report.get('SchemaVersion') != 2 or report.get('ArtifactType') != 'container_image' or not isinstance(report.get('Results'), list) or not report['Results']:
        raise ValueError('Runtime scan is missing verifiable image results')
    fixed = set(); unfixed = set()
    for result in report['Results']:
        for finding in result.get('Vulnerabilities') or []:
            if finding.get('Severity') not in ['HIGH', 'CRITICAL']: continue
            identity = (finding.get('PkgName', ''), finding.get('VulnerabilityID', ''))
            if not all(identity): raise ValueError('Runtime finding identity is incomplete')
            if str(finding.get('FixedVersion') or '').strip(): fixed.add(identity)
            else: unfixed.add(identity)
    return sorted(fixed), sorted(unfixed)

def main():
    try: fixed, unfixed = evaluate(json.loads(pathlib.Path(sys.argv[1]).read_text()))
    except (ValueError, KeyError, IndexError, OSError) as error:
        print('Runtime scan cannot be verified: ' + type(error).__name__)
        return 2
    print('High/critical findings: fixes available=' + str(len(fixed)) + ', no published fix=' + str(len(unfixed)))
    for package, advisory in fixed: print('Release blocked: ' + package + ' ' + advisory)
    return 1 if fixed else 0

if __name__ == '__main__': raise SystemExit(main())
