import unittest

from fizz import fizzbuzz


class FizzTest(unittest.TestCase):
    def test_fifteen(self):
        got = fizzbuzz(15)
        self.assertEqual(len(got), 15)
        self.assertEqual(got[-1], "FizzBuzz")
        self.assertEqual(got[:5], ["1", "2", "Fizz", "4", "Buzz"])


if __name__ == "__main__":
    unittest.main()
