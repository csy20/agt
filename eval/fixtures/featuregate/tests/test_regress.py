import unittest

from greet import greet


class TestRegress(unittest.TestCase):
    def test_nonempty(self):
        self.assertTrue(isinstance(greet(), str) and greet() != "")


if __name__ == "__main__":
    unittest.main()
