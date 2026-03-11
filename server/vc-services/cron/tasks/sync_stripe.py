import dramatiq

from polar.processor_transaction.tasks import sync_stripe  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("processor_transaction.sync_stripe").send()
