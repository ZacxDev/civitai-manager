package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"text/tabwriter"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/spf13/cobra"
)

// searchOptions collects the CLI flags for a model search.
type searchOptions struct {
	query    string
	tag      string
	username string
	kind     string // model type: Checkpoint, LORA, ...
	limit    int
	nsfw     bool
	asJSON   bool
}

// searchValues builds the CivitAI models-search query string from the options.
// Only set flags are included so defaults stay server-side. Exposed (lowercase,
// same package) so tests can assert the exact url.Values without a client.
func searchValues(o searchOptions) url.Values {
	q := url.Values{}
	if o.query != "" {
		q.Set("query", o.query)
	}
	if o.tag != "" {
		q.Set("tag", o.tag)
	}
	if o.username != "" {
		q.Set("username", o.username)
	}
	if o.kind != "" {
		// The models endpoint filters model type via the `types` parameter.
		q.Set("types", o.kind)
	}
	if o.limit > 0 {
		q.Set("limit", strconv.Itoa(o.limit))
	}
	if o.nsfw {
		q.Set("nsfw", "true")
	}
	return q
}

func newSearchCmd(gf *globalFlags) *cobra.Command {
	var o searchOptions
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search CivitAI models from the command line",
		Long: "search queries CivitAI's model catalog and prints a table of matches\n" +
			"(id, name, type, creator, downloads, thumbs-up). Combine the free-text\n" +
			"query with --tag/--username/--type filters, or use them alone. Results are\n" +
			"the first page (or --limit); use --json for the raw API response.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				o.query = args[0]
			}
			if o.query == "" && o.tag == "" && o.username == "" && o.kind == "" {
				return fmt.Errorf("provide a query and/or at least one of --tag/--username/--type")
			}
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			ctx, cancel := signalContext()
			defer cancel()

			return searchRun(ctx, a.client, cmd.OutOrStdout(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.tag, "tag", "", "filter by tag")
	f.StringVar(&o.username, "username", "", "filter by creator username")
	f.StringVar(&o.kind, "type", "", "filter by model type (Checkpoint, LORA, TextualInversion, ...)")
	f.IntVar(&o.limit, "limit", 0, "max results to request (server default when 0)")
	f.BoolVar(&o.nsfw, "nsfw", false, "include NSFW results")
	f.BoolVar(&o.asJSON, "json", false, "print the raw API JSON instead of a table")
	return cmd
}

// searchRun performs the search against the reader and renders the result. It
// takes a civitai.Reader (not the concrete client) so tests drive it with a
// fake and no network.
func searchRun(ctx context.Context, reader civitai.Reader, out io.Writer, o searchOptions) error {
	res, err := reader.SearchModels(ctx, searchValues(o))
	if err != nil {
		return err
	}

	if o.asJSON {
		if len(res.Raw) > 0 {
			_, err := out.Write(res.Raw)
			if err != nil {
				return err
			}
			// Ensure a trailing newline for terminal friendliness.
			if res.Raw[len(res.Raw)-1] != '\n' {
				_, _ = io.WriteString(out, "\n")
			}
			return nil
		}
		// No raw body captured: fall back to nothing rather than a partial render.
		_, err := io.WriteString(out, "{}\n")
		return err
	}

	if len(res.Items) == 0 {
		_, err := io.WriteString(out, "No models found.\n")
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tTYPE\tCREATOR\tDOWNLOADS\tTHUMBS_UP")
	for _, it := range res.Items {
		creator := "-"
		if it.Creator != nil && it.Creator.Username != "" {
			creator = it.Creator.Username
		}
		name := it.Name
		if it.NSFW {
			name += " [NSFW]"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%d\t%d\n",
			it.ID, name, it.Type, creator, it.Stats.DownloadCount, it.Stats.ThumbsUpCount)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	// Pagination hint: the models feed is cursor-paged.
	if cursor := res.Metadata.CursorString(); cursor != "" {
		fmt.Fprintf(out, "\nMore results available. (next cursor: %s)\n", cursor)
	}
	return nil
}
