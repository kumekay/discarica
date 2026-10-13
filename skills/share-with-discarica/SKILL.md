---
name: share-with-discarica
description: Share HTML or Markdown files as web pages via discarica and get back a sharable link, or read pages back (agent-to-agent handover notes). Use when the user asks to share a report, publish a page, send someone an HTML/Markdown document, get a link for a generated file, or leave/read notes for another agent.
---

# Share pages with discarica

discarica hosts HTML and Markdown pages behind unguessable links.
Pages are **private** by default (visible only to their owner after
GitHub/Gitea/Forgejo login); **public** pages are viewable by anyone with the link.
Private pages are readable back through the API, so they work as a notes /
handover space between agents of the same user.

## Setup

Requires two environment variables (ask the user if unset):

```sh
export DISCARICA_URL=https://discarica.kumekay.com   # optional, this is the default
export DISCARICA_TOKEN=dsc_...                       # from the dashboard at $DISCARICA_URL
```

## Upload a page

```sh
curl -sf -X POST "${DISCARICA_URL:-https://discarica.kumekay.com}/api/pages" \
  -H "Authorization: Bearer $DISCARICA_TOKEN" \
  -F "file=@report.html" -F "name=My report"
```

Add `-F "public=true"` to make it viewable without login. Files ending in
`.md` are rendered to HTML on view; everything else is served as HTML.

The JSON response contains the sharable link:

```json
{"id": "...", "name": "My report", "kind": "html", "public": false,
 "url": "https://discarica.kumekay.com/p/NJzHM55az0S5xgWp"}
```

Give the `url` to the user. Markdown pages also return `raw_url` for the
original source. Private URLs use `/p/`, public ones `/pub/`.

## Manage pages

```sh
# List your pages
curl -sf -H "Authorization: Bearer $DISCARICA_TOKEN" "$DISCARICA_URL/api/pages"

# Read a page back, including its content (agent handover notes)
curl -sf -H "Authorization: Bearer $DISCARICA_TOKEN" "$DISCARICA_URL/api/pages/{id}"

# Flip visibility (the url changes between /p/ and /pub/)
curl -sf -X PATCH -H "Authorization: Bearer $DISCARICA_TOKEN" \
  -H "Content-Type: application/json" -d '{"public": true}' \
  "$DISCARICA_URL/api/pages/{id}"

# Delete
curl -sf -X DELETE -H "Authorization: Bearer $DISCARICA_TOKEN" "$DISCARICA_URL/api/pages/{id}"
```

## Rules

- Default to **private** unless the user asks for a public link.
- Report upload failures verbatim (curl exit code and response body); do not retry blindly.
- Never print `DISCARICA_TOKEN` in output shown to the user.
