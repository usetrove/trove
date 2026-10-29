import unittest

from calc import add, double


class CalcTest(unittest.TestCase):
    def test_add(self):
        self.assertEqual(add(2, 3), 5)
        self.assertEqual(add(-1, 1), 0)

    def test_double(self):
        self.assertEqual(double(4), 8)


if __name__ == "__main__":
    unittest.main()
