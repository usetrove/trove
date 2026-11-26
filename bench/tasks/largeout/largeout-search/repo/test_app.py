import unittest
from pkg.core import compute_total
from pkg import mod_07


class T(unittest.TestCase):
    def test(self):
        self.assertEqual(compute_total([1, 2]), 3)
        self.assertEqual(mod_07.f2(), 9)
