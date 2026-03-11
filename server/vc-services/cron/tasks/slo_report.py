import dramatiq

from polar.observability.slo_report.tasks import slo_report_send_weekly  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("slo_report.send_weekly").send()
