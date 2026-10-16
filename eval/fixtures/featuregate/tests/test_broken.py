import unittest


class TestBroken(unittest.TestCase):
    def test_false(self):
        self.assertTrue(False)


if __name__ == "__main__":
    unittest.main()
