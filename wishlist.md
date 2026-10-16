Wishlist:
- Durable repeatable tasks, have something like Airflow (ideally a little lighter) that is used for all scheduled work and as far as possible always create a deterministic script / workflow for things.
- Git is a first class citizen, when you set this up it needs a git repo that it can sync to and I need to be able to branch an agent config and memory.
- Fewer supported agent memory providers and they must also be git compatible, at least have a way to take a snapshot and recover from it.
- Rewrite this in Go to get compiletime errors and reduce the number of test needed