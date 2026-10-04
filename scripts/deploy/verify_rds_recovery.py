"""Read-only production recovery preflight; never creates or deletes backups."""
import argparse
from datetime import datetime, timezone
import json
import os
import subprocess


def timestamp(value):
    if not isinstance(value, str) or not value:
        raise ValueError("Latest recovery timestamp is missing")
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("Recovery timestamps must include a timezone")
    return parsed


def verify(instance, expected_id, now):
    if instance.get("DBInstanceIdentifier") != expected_id:
        raise ValueError("Unexpected database instance")
    if instance.get("DBInstanceStatus") != "available":
        raise ValueError("Database is not available")
    retention = instance.get("BackupRetentionPeriod", 0)
    pending = instance.get("PendingModifiedValues", {}).get("BackupRetentionPeriod")
    if not isinstance(retention, int) or retention < 7 or (pending is not None and pending < 7):
        raise ValueError("Automated backup retention must be at least seven days")
    latest = timestamp(instance.get("LatestRestorableTime", ""))
    lag = (now - latest).total_seconds()
    if lag < -300 or lag > 1800:
        raise ValueError("Automated recovery coverage is missing or more than 30 minutes behind")
    return f"RDS automated recovery verified: retention={retention} days, latest={latest.isoformat()}; no deploy snapshot created"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db-instance-id", required=True)
    parser.add_argument("--region", required=True)
    args = parser.parse_args()
    result = subprocess.run(
        ["aws", "rds", "describe-db-instances", "--db-instance-identifier", args.db_instance_id,
         "--region", args.region, "--output", "json"],
        check=True, capture_output=True, text=True, timeout=45,
    )
    instances = json.loads(result.stdout).get("DBInstances", [])
    if len(instances) != 1:
        raise ValueError("Expected one database instance")
    message = verify(instances[0], args.db_instance_id, datetime.now(timezone.utc))
    print(message)
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as output:
            output.write(message + "\n")


if __name__ == "__main__":
    main()
