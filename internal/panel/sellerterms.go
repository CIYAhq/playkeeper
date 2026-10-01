package panel

import "context"

// sellerTermsVersion is the version of Playkeeper Cloud's seller terms that
// Open the store asks a seller to accept: the date their page on
// playkeeper.io (site/pages/cloud/seller-terms.html) gives as its version.
const sellerTermsVersion = "2026-10-01"

// acceptSellerTerms keeps that user, on the store's business team, accepted
// the seller terms' current version now, unless they already had, with an
// audit entry for a first acceptance.
func (s *Server) acceptSellerTerms(ctx context.Context, storeID, user string) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO whop_terms_accepted(store_id, version, whop_user, accepted_at) VALUES(?,?,?,?)
		ON CONFLICT(store_id, version, whop_user) DO NOTHING`, storeID, sellerTermsVersion, user, s.now().UnixMilli())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		s.audit("whop:"+user, "whop.terms_accept", storeID, "succeeded", "the seller terms of "+sellerTermsVersion)
	}
	return nil
}
