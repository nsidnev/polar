import dramatiq

from polar.transaction.tasks import sync_stripe_fees  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("processor_fee.sync_stripe_fees").send()
