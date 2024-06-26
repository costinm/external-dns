/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package google

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/compute/metadata"
	"github.com/linki/instrumented_http"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2/google"
	dns "google.golang.org/api/dns/v1"
	googleapi "google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"sigs.k8s.io/external-dns/pkg/apis/externaldns"

	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"sigs.k8s.io/external-dns/provider"
)

const (
	googleRecordTTL = 300
)

type managedZonesCreateCallInterface interface {
	Do(opts ...googleapi.CallOption) (*dns.ManagedZone, error)
}

type managedZonesListCallInterface interface {
	Pages(ctx context.Context, f func(*dns.ManagedZonesListResponse) error) error
}

type managedZonesServiceInterface interface {
	Create(project string, managedzone *dns.ManagedZone) managedZonesCreateCallInterface
	List(project string) managedZonesListCallInterface
}

type resourceRecordSetsListCallInterface interface {
	Pages(ctx context.Context, f func(*dns.ResourceRecordSetsListResponse) error) error
}

type resourceRecordSetsClientInterface interface {
	List(project string, managedZone string) resourceRecordSetsListCallInterface
}

type changesCreateCallInterface interface {
	Do(opts ...googleapi.CallOption) (*dns.Change, error)
}

type changesServiceInterface interface {
	Create(project string, managedZone string, change *dns.Change) changesCreateCallInterface
}

type resourceRecordSetsService struct {
	service *dns.ResourceRecordSetsService
}

func (r resourceRecordSetsService) List(project string, managedZone string) resourceRecordSetsListCallInterface {
	return r.service.List(project, managedZone)
}

type managedZonesService struct {
	service *dns.ManagedZonesService
}

func (m managedZonesService) Create(project string, managedzone *dns.ManagedZone) managedZonesCreateCallInterface {
	return m.service.Create(project, managedzone)
}

func (m managedZonesService) List(project string) managedZonesListCallInterface {
	return m.service.List(project)
}

type changesService struct {
	service *dns.ChangesService
}

func (c changesService) Create(project string, managedZone string, change *dns.Change) changesCreateCallInterface {
	return c.service.Create(project, managedZone, change)
}

// GoogleProvider is an implementation of Provider for Google CloudDNS.
type GoogleProvider struct {
	provider.BaseProvider

	externaldns.ProviderConfig

	// Enabled dry-run will print any modifying actions rather than execute them.
	dryRun bool

	// only consider hosted zones managing domains ending in this suffix
	domainFilter *endpoint.DomainFilter

	// filter for zones based on visibility
	zoneTypeFilter provider.ZoneTypeFilter
	// only consider hosted zones ending with this zone id
	zoneIDFilter *provider.ZoneIDFilter

	// A client for managing resource record sets
	resourceRecordSetsClient resourceRecordSetsClientInterface
	// A client for managing hosted zones
	managedZonesClient managedZonesServiceInterface
	// A client for managing change sets
	changesClient changesServiceInterface

	// The context parameter to be passed for gcloud API calls.
	ctx context.Context

	// Cached zone to domain mapping, used if zones are not explicitly set and we
	// need to query. Cached for 30sec (TODO: make it configurable)
	zoneNames          map[string]string
	zoneNamesTimestamp time.Time
}

// NewGoogleProvider initializes a new Google CloudDNS based Provider.
// Will default from environment variables and may use Metadata server
//
// Order:
// - GOOGLE_APPLICATION_CREDENTIALS
// - well known location ~/.config/gcloud/application_default_credentials.json
// - metadata.OnGCE
//   - checks of GCE_METADATA_HOST is set
//   - checks if http://169.254.169.254/ returns Metadata-Flavor=Google or lookup metadata.google.internal. works
//
// Env variables used for config:
// PROJECT_ID - google project ID to use
func NewGoogleProvider(ctx context.Context, cfg *externaldns.ProviderConfig, domainFilter *endpoint.DomainFilter,
	zoneIDFilter *provider.ZoneIDFilter, dryRun bool) (*GoogleProvider, error) {
	dnsClient, err := newGoogleDNSClient(ctx)
	if err != nil {
		return nil, err
	}

	zoneVisibility := cfg.GoogleZoneVisibility

	if cfg.GoogleProject == "" {
		cfg.GoogleProject = os.Getenv("PROJECT_ID")
	}
	if cfg.GoogleProject == "" {
		cfg.GoogleProject = os.Getenv("GOOGLE_PROJECT_ID")
	}
	if cfg.GoogleProject == "" {
		mProject, mErr := metadata.ProjectID()
		if mErr != nil {
			return nil, fmt.Errorf("failed to auto-detect the project id: %w", mErr)
		}
		log.Infof("Google project auto-detected: %s", mProject)
		cfg.GoogleProject = mProject
	}
	if domainFilter == nil {
		df := endpoint.NewDomainFilter([]string{})
		domainFilter = &df
	}
	if zoneIDFilter == nil {
		df := provider.NewZoneIDFilter([]string{})
		zoneIDFilter = &df
	}

	gprovider := &GoogleProvider{
		ProviderConfig:        *cfg,
		dryRun:                   dryRun,
		domainFilter:             domainFilter,
		zoneIDFilter:             zoneIDFilter,
		resourceRecordSetsClient: resourceRecordSetsService{dnsClient.ResourceRecordSets},
		managedZonesClient:       managedZonesService{dnsClient.ManagedZones},
		changesClient:            changesService{dnsClient.Changes},
		ctx:                      ctx,
	}

	if zoneVisibility != "" {
		gprovider.zoneTypeFilter = provider.NewZoneTypeFilter(zoneVisibility)
	}

	if gprovider.ProviderConfig.Zones == nil {
		// Query the zones once. Should be cached
		zones, err := gprovider.Zones(ctx)
		if err != nil {
			return nil, err
		}
		for _, z := range zones {
			log.Info("Zone", " name", z.Name, " dns=", z.DnsName, " visibility=", z.Visibility)
		}
	}

	return gprovider, nil
}

// newGoogleDNSClient returns a client for Google DNS, using defaults.
func newGoogleDNSClient(ctx context.Context) (*dns.Service, error) {
	gcloud, err := google.DefaultClient(ctx, dns.NdevClouddnsReadwriteScope)
	if err != nil {
		return nil, err
	}
	// This is used by external_dns for prometheus.
	gcloud = instrumented_http.NewClient(gcloud, &instrumented_http.Callbacks{
		PathProcessor: func(path string) string {
			parts := strings.Split(path, "/")
			return parts[len(parts)-1]
		},
	})

	dnsClient, err := dns.NewService(ctx, option.WithHTTPClient(gcloud))
	if err != nil {
		return nil, err
	}
	return dnsClient, nil
}

// Zone2Domain returns the map of zone name to corresponding domain.
// It will return the user-configured map if provided, or query the zones in the project
// otherwise. The result is cached to avoid churn.
//
// User may not have permissions to list the zones or access other zones - IAM can be granted to zones.
func (p *GoogleProvider) Zone2Domain(ctx context.Context) (map[string]string, error) {
	if p.ProviderConfig.Zones != nil {
		// Explicitly set by user - probably no permissions to list zones or user doesn't want all zones.
		return p.ProviderConfig.Zones, nil
	}
	if p.zoneNames != nil && time.Since(p.zoneNamesTimestamp) < 30*time.Second {
		return p.zoneNames, nil
	}
	z, err := p.Zones(ctx)
	if err != nil {
		return nil, err
	}
	p.zoneNames = map[string]string{}

	for _, zi := range z {
		p.zoneNames[zi.Name] = zi.DnsName
	}
	p.zoneNamesTimestamp = time.Now()
	return p.zoneNames, nil
}

func (p *GoogleProvider) GetDomainFilter() endpoint.DomainFilter {
	df := endpoint.DomainFilter{}
	z2d, err := p.Zone2Domain(context.Background())
	if err != nil {
		// Should not happen - it is loaded at startup and updated if possible.
		return df
	}
	for _, d := range z2d {
		df.Filters = append(df.Filters, d)
	}
	return df
}


// Zones returns the list of hosted zones, using the domainFilter, zoneTypeFilter, zoneIDFilter
// to limit the results.
func (p *GoogleProvider) Zones(ctx context.Context) (map[string]*dns.ManagedZone, error) {
	zones := make(map[string]*dns.ManagedZone)

	// GKE zones are named gke-CLUSTERNAME-HASH-dns
	// Description is like "Private zone for GKE cluster "CLUSTER_NAME" with cluster suffix "cluster.local." in project "PROJECT_ID" with scope "CLUSTER_SCOPE
	// They have PrivateVisibilityConfig set to GkeCluster with the full cluster name included.

	f := func(resp *dns.ManagedZonesListResponse) error {
		for _, zone := range resp.ManagedZones {
			if zone.PeeringConfig != nil {
				log.Debugf("Filtered peering zone %s (zone: %s) (visibility: %s)", zone.DnsName, zone.Name, zone.Visibility)
				continue
			}
			if strings.HasPrefix(zone.Name, "gke-") {
				log.Debugf("Filtered gke zone %s (zone: %s) (visibility: %s)", zone.DnsName, zone.Name, zone.Visibility)
				continue
			}
			if p.domainFilter.Match(zone.DnsName) && p.zoneTypeFilter.Match(zone.Visibility) && (p.zoneIDFilter.Match(fmt.Sprintf("%v", zone.Id)) || p.zoneIDFilter.Match(fmt.Sprintf("%v", zone.Name))) {
				zones[zone.Name] = zone
				log.Debugf("Matched %s (zone: %s) (visibility: %s)", zone.DnsName, zone.Name, zone.Visibility)
			} else {
				log.Debugf("Filtered %s (zone: %s) (visibility: %s)", zone.DnsName, zone.Name, zone.Visibility)
			}
		}

		return nil
	}

	log.Debugf("Matching zones against domain filters: %v", p.domainFilter)
	if err := p.managedZonesClient.List(p.GoogleProject).Pages(ctx, f); err != nil {
		return nil, err
	}

	if len(zones) == 0 {
		log.Warnf("No zones in the project, %s, match domain filters: %v", p.GoogleProject, p.domainFilter)
	}

	for _, zone := range zones {
		log.Debugf("Considering zone: %s (domain: %s)", zone.Name, zone.DnsName)
	}

	// TODO: filter out .cluster.local zones and other GKE-reconciled zones.

	return zones, nil
}

// Records returns the list of records in all relevant zones.
func (p *GoogleProvider) Records(ctx context.Context) (endpoints []*endpoint.Endpoint, _ error) {
	f := func(resp *dns.ResourceRecordSetsListResponse) error {
		for _, r := range resp.Rrsets {
			if !p.SupportedRecordType(r.Type) {
				continue
			}
			// May also include Singatures
			endpoints = append(endpoints, endpoint.NewEndpointWithTTL(r.Name, r.Type, endpoint.TTL(r.Ttl), r.Rrdatas...))
		}

		return nil
	}

	zones, err := p.Zone2Domain(ctx)
	if err != nil {
		return nil, err
	}

	for n, _ := range zones {
		if err := p.resourceRecordSetsClient.List(p.GoogleProject, n).Pages(ctx, f); err != nil {
			return nil, err
		}
	}

	return endpoints, nil
}

// ApplyChanges applies a given set of changes in a given zone. Only DNS domains that are configured are allowed.
func (p *GoogleProvider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	change := &dns.Change{}

	change.Additions = append(change.Additions, p.newFilteredRecords(changes.Create)...)

	change.Additions = append(change.Additions, p.newFilteredRecords(changes.UpdateNew)...)
	change.Deletions = append(change.Deletions, p.newFilteredRecords(changes.UpdateOld)...)

	change.Deletions = append(change.Deletions, p.newFilteredRecords(changes.Delete)...)

	return p.submitChange(ctx, change)
}

// SupportedRecordType returns true if the record type is supported by the provider
func (p *GoogleProvider) SupportedRecordType(recordType string) bool {
	switch recordType {
	case "MX":
		return true
	default:
		return provider.SupportedRecordType(recordType)
	}
}

// newFilteredRecords returns a collection of RecordSets based on the given endpoints and domainFilter.
func (p *GoogleProvider) newFilteredRecords(endpoints []*endpoint.Endpoint) []*dns.ResourceRecordSet {
	records := []*dns.ResourceRecordSet{}

	for _, endpoint := range endpoints {
		if p.domainFilter.Match(endpoint.DNSName) {
			records = append(records, newRecord(endpoint))
		}
	}

	return records
}

// submitChange takes a zone and a Change and sends it to Google.
func (p *GoogleProvider) submitChange(ctx context.Context, change *dns.Change) error {
	if len(change.Additions) == 0 && len(change.Deletions) == 0 {
		log.Debug("All records are already up to date")
		return nil
	}

	zones, err := p.Zone2Domain(ctx)
	if err != nil {
		return err
	}

	// separate into per-zone change sets to be passed to the domain name.
	changes := separateChange(zones, change)

	for zone, change := range changes {
		for batch, c := range batchChange(change, p.GoogleBatchChangeSize) {
			log.Infof("Change zone: %v batch #%d", zone, batch)
			for _, del := range c.Deletions {
				log.Infof("Del records: %s %s %s %d", del.Name, del.Type, del.Rrdatas, del.Ttl)
			}
			for _, add := range c.Additions {
				log.Infof("Add records: %s %s %s %d", add.Name, add.Type, add.Rrdatas, add.Ttl)
			}

			if p.dryRun {
				continue
			}

			if _, err := p.changesClient.Create(p.GoogleProject, zone, c).Do(); err != nil {
				return err
			}

			time.Sleep(p.GoogleBatchChangeInterval)
		}
	}

	return nil
}

// batchChange separates a zone in multiple transaction.
func batchChange(change *dns.Change, batchSize int) []*dns.Change {
	changes := []*dns.Change{}

	if batchSize == 0 {
		return append(changes, change)
	}

	type dnsChange struct {
		additions []*dns.ResourceRecordSet
		deletions []*dns.ResourceRecordSet
	}

	changesByName := map[string]*dnsChange{}

	for _, a := range change.Additions {
		change, ok := changesByName[a.Name]
		if !ok {
			change = &dnsChange{}
			changesByName[a.Name] = change
		}

		change.additions = append(change.additions, a)
	}

	for _, a := range change.Deletions {
		change, ok := changesByName[a.Name]
		if !ok {
			change = &dnsChange{}
			changesByName[a.Name] = change
		}

		change.deletions = append(change.deletions, a)
	}

	names := make([]string, 0)
	for v := range changesByName {
		names = append(names, v)
	}
	sort.Strings(names)

	currentChange := &dns.Change{}
	var totalChanges int
	for _, name := range names {
		c := changesByName[name]

		totalChangesByName := len(c.additions) + len(c.deletions)

		if totalChangesByName > batchSize {
			log.Warnf("Total changes for %s exceeds max batch size of %d, total changes: %d", name,
				batchSize, totalChangesByName)
			continue
		}

		if totalChanges+totalChangesByName > batchSize {
			totalChanges = 0
			changes = append(changes, currentChange)
			currentChange = &dns.Change{}
		}

		currentChange.Additions = append(currentChange.Additions, c.additions...)
		currentChange.Deletions = append(currentChange.Deletions, c.deletions...)

		totalChanges += totalChangesByName
	}

	if totalChanges > 0 {
		changes = append(changes, currentChange)
	}

	return changes
}

// separateChange separates a multi-zone change into a single change per zone.
func separateChange(zones map[string]string, change *dns.Change) map[string]*dns.Change {
	changes := make(map[string]*dns.Change)
	zoneNameIDMapper := provider.ZoneIDName{}
	for n, z := range zones {
		zoneNameIDMapper[n] = z
		changes[n] = &dns.Change{
			Additions: []*dns.ResourceRecordSet{},
			Deletions: []*dns.ResourceRecordSet{},
		}
	}
	for _, a := range change.Additions {
		if zoneName, _ := zoneNameIDMapper.FindZone(provider.EnsureTrailingDot(a.Name)); zoneName != "" {
			changes[zoneName].Additions = append(changes[zoneName].Additions, a)
		} else {
			log.Warnf("No matching zone for record addition: %s %s %s %d", a.Name, a.Type, a.Rrdatas, a.Ttl)
		}
	}

	for _, d := range change.Deletions {
		if zoneName, _ := zoneNameIDMapper.FindZone(provider.EnsureTrailingDot(d.Name)); zoneName != "" {
			changes[zoneName].Deletions = append(changes[zoneName].Deletions, d)
		} else {
			log.Warnf("No matching zone for record deletion: %s %s %s %d", d.Name, d.Type, d.Rrdatas, d.Ttl)
		}
	}

	// separating a change could lead to empty sub changes, remove them here.
	for zone, change := range changes {
		if len(change.Additions) == 0 && len(change.Deletions) == 0 {
			delete(changes, zone)
		}
	}

	return changes
}

// newRecord returns a RecordSet based on the given endpoint.
func newRecord(ep *endpoint.Endpoint) *dns.ResourceRecordSet {
	// TODO(linki): works around appending a trailing dot to TXT records. I think
	// we should go back to storing DNS names with a trailing dot internally. This
	// way we can use it has is here and trim it off if it exists when necessary.
	targets := make([]string, len(ep.Targets))
	copy(targets, []string(ep.Targets))

	if ep.RecordType == endpoint.RecordTypeCNAME {
		targets[0] = provider.EnsureTrailingDot(targets[0])
	}

	if ep.RecordType == endpoint.RecordTypeMX {
		for i, mxRecord := range ep.Targets {
			targets[i] = provider.EnsureTrailingDot(mxRecord)
		}
	}

	if ep.RecordType == endpoint.RecordTypeSRV {
		for i, srvRecord := range ep.Targets {
			targets[i] = provider.EnsureTrailingDot(srvRecord)
		}
	}

	// no annotation results in a Ttl of 0, default to 300 for backwards-compatibility
	var ttl int64 = googleRecordTTL
	if ep.RecordTTL.IsConfigured() {
		ttl = int64(ep.RecordTTL)
	}

	return &dns.ResourceRecordSet{
		Name:    provider.EnsureTrailingDot(ep.DNSName),
		Rrdatas: targets,
		Ttl:     ttl,
		Type:    ep.RecordType,
	}
}
