# testing the bundles branch against your normal hermes install

instructions:

1. close hermes, the gateway, etc. make sure you have no running hermes processes.

2. apply my updater override:
   ```bash
   REPO_ID="NousResearch/hermes-agent"
   SHA=$(curl -fsSL -H "User-Agent: hermes-installer" "https://api.github.com/repos/$REPO_ID/commits/main" | grep -m1 '"sha"' | cut -d'"' -f4)
   if [[ ! "$SHA" =~ ^[0-9a-f]{40}$ ]]; then echo "Failed to resolve commit SHA" >&2; exit 1; fi
   curl -fsSL "https://raw.githubusercontent.com/$REPO_ID/$SHA/scripts/update-test/hermes-update-rehearsal.sh" | bash -s -- pre
   ```

   this backs up your entire hermes home and any desktop app settings, then points your install's updates at the test fork's `main` (no local mirror: `hermes update` fetches straight from the fork, so it needs network). _from this point on, nothing you do in hermes will be preserved, until you restore your backup at the end._

3. boot hermes up to ensure everything is working, still. if you normally have a background service, gateway, etc, make sure it's running.

4. update hermes like you normally do.

5. test hermes. make sure nothing breaks, everything you use still works, etc.

6. close hermes, the gateway, etc. make sure you have no running hermes processes.

7. restore your backup:

   ```bash
   REPO_ID="NousResearch/hermes-agent"
   SHA=$(curl -fsSL -H "User-Agent: hermes-installer" "https://api.github.com/repos/$REPO_ID/commits/main" | grep -m1 '"sha"' | cut -d'"' -f4)
   if [[ ! "$SHA" =~ ^[0-9a-f]{40}$ ]]; then echo "Failed to resolve commit SHA" >&2; exit 1; fi
   curl -fsSL "https://raw.githubusercontent.com/$REPO_ID/$SHA/scripts/update-test/hermes-update-rehearsal.sh" | bash -s -- post --yes
   ```

   this puts hermes back to exactly how it was beforehand.

# testing the bundles branch from a fresh install

in a terminal

```bash
REPO_ID="NousResearch/hermes-agent"

SHA=$(curl -fsSL -H "User-Agent: hermes-installer" "https://api.github.com/repos/$REPO_ID/commits/main" | grep -m1 '"sha"' | cut -d'"' -f4)

if [[ ! "$SHA" =~ ^[0-9a-f]{40}$ ]]; then
  echo "Failed to resolve commit SHA for $REPO_ID" >&2
  exit 1
fi

curl -fsSL "https://raw.githubusercontent.com/$REPO_ID/$SHA/scripts/install.sh" | HERMES_REPO_URL="https://github.com/$REPO_ID.git" bash
```
