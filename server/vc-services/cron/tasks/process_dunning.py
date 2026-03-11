import dramatiq

from polar.order.tasks import process_dunning  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("order.process_dunning").send()
