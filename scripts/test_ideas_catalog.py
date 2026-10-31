"""The depth catalog is generated, so it can drift from its seed.

docs/IDEAS.md is what a reader opens; seed/concord_ideas.json is what file_ideas.py uploads to the
instance. If the two disagree, the document describes a site that does not exist -- the same class
of failure as a spec two revisions behind the code, and just as invisible to every Go test.

Run by `make docs-check` (unittest discovery over scripts/test_*.py).
"""
import json
import pathlib
import subprocess
import sys
import unittest

REPO = pathlib.Path(__file__).resolve().parent.parent
SEED = REPO / "seed" / "concord_ideas.json"
DOC = REPO / "docs" / "IDEAS.md"
EFFORTS = {"S", "M", "L", "XL"}


class IdeasCatalogTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.data = json.loads(SEED.read_text())
        cls.ideas = cls.data["ideas"]
        cls.themes = cls.data["themes"]

    def test_doc_is_rendered_from_seed(self):
        out = subprocess.run(
            [sys.executable, str(REPO / "scripts" / "render_ideas.py"), "--check"],
            capture_output=True, text=True,
        )
        self.assertEqual(out.returncode, 0, out.stderr.strip())

    def test_every_theme_has_a_complaint_and_ideas(self):
        keys = {t["key"] for t in self.themes}
        self.assertEqual(set(self.ideas), keys,
                         "ideas and themes must name the same keys")
        for t in self.themes:
            for field in ("label", "complaint_title", "complaint_body"):
                self.assertTrue(t[field].strip(), f"{t['key']} has an empty {field}")
            self.assertGreaterEqual(len(self.ideas[t["key"]]), 1, t["key"])

    def test_complaint_titles_are_unique(self):
        titles = [t["complaint_title"] for t in self.themes]
        self.assertEqual(len(titles), len(set(titles)))

    def test_idea_titles_are_unique_and_well_formed(self):
        seen = {}
        for key, items in self.ideas.items():
            for idea in items:
                self.assertIn(idea["effort"], EFFORTS, idea["title"])
                self.assertGreater(len(idea["title"]), 10, idea["title"])
                self.assertGreater(len(idea["body"]), 40, idea["title"])
                self.assertNotIn(idea["title"], seen,
                                 f"duplicate title: {idea['title']}")
                seen[idea["title"]] = key

    def test_catalog_is_large_enough_to_be_a_catalog(self):
        total = sum(len(v) for v in self.ideas.values())
        self.assertGreaterEqual(total, 300, f"only {total} ideas")

    def test_rendered_doc_lists_every_idea(self):
        text = DOC.read_text()
        for items in self.ideas.values():
            for idea in items:
                self.assertIn(idea["title"], text, idea["title"])


if __name__ == "__main__":
    unittest.main()
