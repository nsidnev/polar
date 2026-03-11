import dramatiq

from polar.email_update.tasks import email_update_delete_expired_record  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("email_update.delete_expired_record").send()
