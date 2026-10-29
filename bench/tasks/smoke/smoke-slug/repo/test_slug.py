import unittest

from slug import slugify


class SlugTest(unittest.TestCase):
    def test_lowercase(self):
        self.assertEqual(slugify("Hello World"), "hello-world")

    def test_collapses_spaces(self):
        self.assertEqual(slugify("a   b"), "a-b")

    def test_trims(self):
        self.assertEqual(slugify("  Trim Me "), "trim-me")


if __name__ == "__main__":
    unittest.main()
