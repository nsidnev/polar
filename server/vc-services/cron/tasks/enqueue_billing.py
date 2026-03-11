import dramatiq

from polar.meter.tasks import meter_enqueue_billing  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("meter.enqueue_billing").send()
