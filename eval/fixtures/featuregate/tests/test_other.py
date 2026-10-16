import unittest


class TestOther(unittest.TestCase):
    def test_marker(self):
        self.assertEqual(1, 1)


if __name__ == "__main__":
    unittest.main()
