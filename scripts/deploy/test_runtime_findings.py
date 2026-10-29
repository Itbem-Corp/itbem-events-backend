import importlib.util, pathlib, unittest
spec = importlib.util.spec_from_file_location('runtime_findings', pathlib.Path(__file__).with_name('check-runtime-findings.py'))
gate = importlib.util.module_from_spec(spec); spec.loader.exec_module(gate)

class RuntimeFindingTests(unittest.TestCase):
    def report(self, findings):
        return {'SchemaVersion': 2, 'ArtifactType': 'container_image', 'Results': [{'Target': 'app/main', 'Vulnerabilities': findings}]}
    def test_available_critical_patch_blocks_release(self):
        fixed, unfixed = gate.evaluate(self.report([{'PkgName': 'framework', 'VulnerabilityID': 'CVE-test', 'Severity': 'CRITICAL', 'FixedVersion': '2.0'}]))
        self.assertEqual(fixed, [('framework', 'CVE-test')]); self.assertFalse(unfixed)
    def test_unfixed_findings_are_reported_separately(self):
        fixed, unfixed = gate.evaluate(self.report([{'PkgName': 'library', 'VulnerabilityID': 'CVE-test', 'Severity': 'HIGH', 'FixedVersion': ''}]))
        self.assertFalse(fixed); self.assertEqual(unfixed, [('library', 'CVE-test')])
    def test_missing_or_incomplete_results_fail_closed(self):
        for invalid in [{}, self.report([]) | {'Results': []}, self.report([{'Severity': 'HIGH', 'FixedVersion': '2.0'}])]:
            with self.assertRaises(ValueError): gate.evaluate(invalid)

if __name__ == '__main__': unittest.main()
