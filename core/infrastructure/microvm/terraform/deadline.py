#!/usr/bin/env python3
"""Terraform external data: reject unsafe explicit host deadlines without cloud I/O."""
import json
import sys
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo

query = json.load(sys.stdin)
now = datetime.now(timezone.utc)
local_now = now.astimezone(ZoneInfo("America/Los_Angeles"))
cutoff = local_now.replace(hour=22, minute=0, second=0, microsecond=0)
try:
    deadline = datetime.fromisoformat(query["termination_time"].replace("Z", "+00:00"))
    valid = (
        deadline.tzinfo is not None
        and now < deadline <= now + timedelta(minutes=30)
        and deadline <= cutoff
    )
except (KeyError, TypeError, ValueError):
    valid = False
print(json.dumps({"valid": str(valid).lower(), "cutoff": cutoff.isoformat()}))
