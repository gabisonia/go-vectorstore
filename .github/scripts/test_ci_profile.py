import subprocess
import unittest
from unittest.mock import patch

import ci_profile as ci


def edit(path, status=b"M", old=b"100644", new=b"100644"):
    return b":" + old + b" " + new + b" abc def " + status + b"\0" + path + b"\0"


class ProfileTests(unittest.TestCase):
    def test_only_edits_to_known_documents_qualify(self):
        docs = edit(b"docs/model-api.md") + edit(b"README.md")
        self.assertEqual(ci.classify(docs), "prose")
        for diff in (b"", docs[:-1], docs + edit(b"vectordata/model.go"),
                     edit(b"docs/new.md"), edit(b"docs/benchmarks/input.md"),
                     edit(b"README.md", old=b"120000"), edit(b"README.md", new=b"100755"),
                     edit(b"docs/model-api.md", status=b"D"), edit(b"README.md\nscript.go")):
            with self.subTest(diff=diff):
                self.assertEqual(ci.classify(diff), "full")

    @patch("ci_profile.subprocess.run")
    def test_only_pr_events_with_a_successful_diff_can_use_prose(self, run):
        sha = "a" * 40
        for event in ("push", "workflow_dispatch", "workflow_call"):
            self.assertEqual(ci.profile(event, sha, sha), "full")
        self.assertEqual(ci.profile("pull_request", "", sha), "full")
        run.assert_not_called()
        run.return_value = subprocess.CompletedProcess([], 1, stdout=edit(b"README.md"))
        self.assertEqual(ci.profile("pull_request", sha, sha), "full")
        run.return_value.returncode = 0
        self.assertEqual(ci.profile("pull_request", sha, sha), "prose")

    def jobs(self, profile):
        jobs = {name: {"result": "success"} for name in ci.PROSE_REQUIRED | ci.FULL_ONLY}
        if profile == "prose":
            for name in ci.FULL_ONLY:
                jobs[name]["result"] = "skipped"
        jobs["profile"]["outputs"] = {"profile": profile}
        return jobs

    def test_gate_checks_both_profiles_and_every_failure(self):
        for profile in ("prose", "full"):
            self.assertEqual(ci.verify(self.jobs(profile)), profile)
            for name in self.jobs(profile):
                for result in ("failure", "cancelled"):
                    jobs = self.jobs(profile)
                    jobs[name]["result"] = result
                    with self.subTest(profile=profile, name=name, result=result), self.assertRaises(ValueError):
                        ci.verify(jobs)

    def test_gate_rejects_missing_dependencies_and_unexpected_skips(self):
        for profile, name in (("full", "fuzz"), ("prose", "test"), ("prose", "new-job")):
            jobs = self.jobs(profile)
            jobs[name] = {"result": "skipped"}
            with self.assertRaises(ValueError):
                ci.verify(jobs)
        jobs = self.jobs("prose")
        del jobs["integration-redis"]
        with self.assertRaises(ValueError):
            ci.verify(jobs)
        with self.assertRaises(ValueError):
            ci.verify(self.jobs("unknown"))


if __name__ == "__main__":
    unittest.main()
