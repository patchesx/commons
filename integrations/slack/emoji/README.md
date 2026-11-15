standard.json is the standard emoji set the composer's picker and preview, and the poll option validation, use.
It is built from iamcal/emoji-data v15.1.2 (MIT, see LICENSE-emoji-data), whose short names are the ones Slack
uses, with search terms added from GitHub's gemoji v4.1.0 (MIT, see LICENSE-gemoji) where the same emoji is in
both. Each entry is `e` (character), `n` (shortcodes, the first is the main one), `t` (search terms) and `c`
(category). GitHub's names alone were not enough: hundreds of Slack names, such as `ballot_box_with_ballot` or
`cheese_wedge`, are not in gemoji.

To update, download both datasets and run, from this directory:

    go run generate.go -iamcal path/to/emoji-data/emoji.json -gemoji path/to/gemoji/db/emoji.json -out standard.json
