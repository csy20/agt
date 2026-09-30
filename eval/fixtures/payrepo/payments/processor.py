"""Payment charging."""


class PaymentProcessor:
    def __init__(self):
        self.ledger = []

    def charge(self, idempotency_key: str, amount: int) -> int:
        self.ledger.append((idempotency_key, amount))
        return len(self.ledger)
