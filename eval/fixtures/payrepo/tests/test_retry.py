import unittest

from payments.processor import PaymentProcessor


class RetryTest(unittest.TestCase):
    def test_retry_does_not_double_charge(self):
        processor = PaymentProcessor()
        first = processor.charge("k1", 100)
        second = processor.charge("k1", 100)
        self.assertEqual(first, second)
        self.assertEqual(len(processor.ledger), 1)


if __name__ == "__main__":
    unittest.main()
