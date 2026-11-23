# Ember downstream calls

When Ember fans out a notification it needs the recipient's delivery preferences. Ember calls the preferences API Parch before it chooses a channel. Parch answers from an in-memory copy that refreshes every minute, so the call is cheap. If Parch is unavailable Ember falls back to email only and logs a warning. This fallback was added after a holiday campaign overloaded the push provider.
