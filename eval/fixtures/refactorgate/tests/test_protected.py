import unittest

from greet import greet


class TestProtected(unittest.TestCase):
    def test_hello(self):
        self.assertEqual(greet(), "hello")


if __name__ == "__main__":
    unittest.main()
