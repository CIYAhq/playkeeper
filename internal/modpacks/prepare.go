package modpacks

import "context"

// Prepared is an install plan kept with its pack's archive, for a caller
// with work to do between planning and installing, such as installing the
// server software the pack needs, that should not download the archive
// twice. Close it when done.
type Prepared struct {
	// Plan is what Apply carries out.
	Plan *Plan
	l    *Library
	srv  Server
	req  InstallRequest
}

// PrepareInstall plans installing a pack on a server like PlanInstall and
// keeps the plan for Apply.
func (l *Library) PrepareInstall(ctx context.Context, srv Server, current *Record, req InstallRequest) (*Prepared, error) {
	pl, err := l.planInstall(ctx, srv, current, req)
	if err != nil {
		return nil, err
	}
	return &Prepared{Plan: pl, l: l, srv: srv, req: req}, nil
}

// Apply installs the prepared plan like Install. It plans again from the
// kept archive, as the server's folder may have changed since, and refuses
// when that plan differs from the prepared one.
func (p *Prepared) Apply(ctx context.Context) (*Result, error) {
	root, err := openServer(p.srv)
	if err != nil {
		return nil, err
	}
	pl, err := p.l.plan(root, p.srv, p.Plan.pk, nil, p.req.Include, p.req.Keep)
	root.Close()
	if err != nil {
		return nil, err
	}
	if pl.Fingerprint != p.Plan.Fingerprint {
		return nil, planChanged()
	}
	return p.l.apply(ctx, p.srv, pl, p.req.OnProgress)
}

// Close removes the kept archive.
func (p *Prepared) Close() { p.Plan.pk.close() }
