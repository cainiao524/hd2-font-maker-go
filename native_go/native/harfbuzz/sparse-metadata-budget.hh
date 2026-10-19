// Sparse custom GIDs create real metadata slots for the intervening holes.
// Allow bounded room for those slots without relaxing unrelated table limits.
static unsigned
_hd2_sparse_metadata_budget (hb_subset_plan_t *plan, hb_tag_t tag)
{
  const unsigned slots = plan->num_output_glyphs ();
  if (slots > 65535u || slots <= plan->glyphset ()->get_population ())
    return 0;

  switch (tag)
  {
  case HB_TAG ('h','m','t','x'):
  case HB_TAG ('v','m','t','x'):
  case HB_TAG ('l','o','c','a'):
  case HB_TAG ('p','o','s','t'):
    // These arrays use at most four bytes per slot; leave 2x room plus the
    // normal serializer allowance. Source-sized name payloads retain the
    // existing estimate and the existing source_length * 256 retry bound.
    return 8192u + slots * 8u;
  default:
    return 0;
  }
}
