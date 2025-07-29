package cmd

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"

	"github.com/mattsolo1/grove-core/config"
	"github.com/grovepm/grove-jobs/pkg/orchestration"
)

type JobsGraphCmd struct {
	Dir    string `arg:"" help:"Plan directory"`
	Format string `flag:"f" default:"mermaid" help:"Output format: mermaid, dot, ascii"`
	Serve  bool   `flag:"s" help:"Serve interactive HTML visualization"`
	Port   int    `flag:"p" default:"8080" help:"Port for web server"`
	Output string `flag:"o" help:"Output file (stdout if not specified)"`
}

func (c *JobsGraphCmd) Run() error {
	return RunJobsGraph(c)
}

func RunJobsGraph(cmd *JobsGraphCmd) error {
	// Load config to check for PlansDirectory setting
	cwd, _ := os.Getwd()
	configFile, err := config.FindConfigFile(cwd)
	var cfg *config.Config
	if err == nil {
		cfg, err = config.LoadWithOverrides(configFile)
		if err != nil {
			cfg = &config.Config{}
		}
	} else {
		cfg = &config.Config{}
	}

	// Resolve the plan path with active job support
	planPath, err := resolvePlanPathWithActiveJob(cmd.Dir, cfg)
	if err != nil {
		return fmt.Errorf("could not resolve plan path: %w", err)
	}

	// Load plan
	plan, err := orchestration.LoadPlan(planPath)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	if len(plan.Jobs) == 0 {
		return fmt.Errorf("no jobs found in plan")
	}

	// Build dependency graph
	graph := buildDependencyGraph(plan)

	// Handle serve mode
	if cmd.Serve {
		return serveInteractiveGraph(plan, graph, cmd.Port)
	}

	// Generate graph in requested format
	var output string
	switch cmd.Format {
	case "mermaid":
		output = generateMermaidGraph(plan, graph)
	case "dot":
		output = generateDotGraph(plan, graph)
	case "ascii":
		output = generateASCIIGraph(plan, graph)
	default:
		return fmt.Errorf("invalid format: use mermaid, dot, or ascii")
	}

	// Write output
	if cmd.Output != "" {
		if err := os.WriteFile(cmd.Output, []byte(output), 0644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		fmt.Printf("Graph written to %s\n", cmd.Output)
	} else {
		fmt.Println(output)
	}

	return nil
}

type DependencyGraph struct {
	Nodes map[string]*orchestration.Job
	Edges map[string][]string // job ID -> list of dependent job IDs
	Roots []string            // Jobs with no dependencies
}

func buildDependencyGraph(plan *orchestration.Plan) *DependencyGraph {
	graph := &DependencyGraph{
		Nodes: make(map[string]*orchestration.Job),
		Edges: make(map[string][]string),
		Roots: []string{},
	}

	// Add all nodes
	for _, job := range plan.Jobs {
		graph.Nodes[job.ID] = job
	}

	// Build edges and find roots
	for _, job := range plan.Jobs {
		if len(job.Dependencies) == 0 {
			graph.Roots = append(graph.Roots, job.ID)
		}
		
		// For each dependency, add an edge from dependency to this job
		for _, dep := range job.Dependencies {
			if dep != nil {
				graph.Edges[dep.ID] = append(graph.Edges[dep.ID], job.ID)
			}
		}
	}

	return graph
}

func generateMermaidGraph(plan *orchestration.Plan, graph *DependencyGraph) string {
	var buf strings.Builder
	
	buf.WriteString("graph TD\n")
	
	// Add nodes
	for _, job := range plan.Jobs {
		nodeID := strings.ReplaceAll(job.ID, "-", "_")
		label := fmt.Sprintf("%s<br/>%s", job.Filename, getStatusSymbol(job.Status))
		buf.WriteString(fmt.Sprintf("    %s[%s]\n", nodeID, label))
	}
	
	buf.WriteString("\n")
	
	// Add edges
	for _, job := range plan.Jobs {
		nodeID := strings.ReplaceAll(job.ID, "-", "_")
		for _, dep := range job.Dependencies {
			if dep != nil {
				depNodeID := strings.ReplaceAll(dep.ID, "-", "_")
				buf.WriteString(fmt.Sprintf("    %s --> %s\n", depNodeID, nodeID))
			}
		}
	}
	
	buf.WriteString("\n")
	
	// Add style classes
	buf.WriteString("    classDef completed fill:#90EE90,stroke:#228B22\n")
	buf.WriteString("    classDef running fill:#FFD700,stroke:#FFA500\n")
	buf.WriteString("    classDef pending fill:#D3D3D3,stroke:#696969\n")
	buf.WriteString("    classDef failed fill:#FFB6C1,stroke:#DC143C\n")
	buf.WriteString("    classDef blocked fill:#DDA0DD,stroke:#8B008B\n")
	buf.WriteString("    classDef needs_review fill:#87CEEB,stroke:#4682B4\n")
	
	buf.WriteString("\n")
	
	// Apply classes to nodes
	statusGroups := make(map[orchestration.JobStatus][]string)
	for _, job := range plan.Jobs {
		nodeID := strings.ReplaceAll(job.ID, "-", "_")
		statusGroups[job.Status] = append(statusGroups[job.Status], nodeID)
	}
	
	for status, nodes := range statusGroups {
		if len(nodes) > 0 {
			className := getStatusClass(status)
			buf.WriteString(fmt.Sprintf("    class %s %s\n", strings.Join(nodes, ","), className))
		}
	}
	
	return buf.String()
}

func generateDotGraph(plan *orchestration.Plan, graph *DependencyGraph) string {
	var buf strings.Builder
	
	buf.WriteString("digraph jobs {\n")
	buf.WriteString("    rankdir=TD;\n")
	buf.WriteString("    node [shape=box, style=rounded];\n\n")
	
	// Add nodes
	for _, job := range plan.Jobs {
		label := fmt.Sprintf("%s\\n%s", job.Filename, string(job.Status))
		color := getStatusColor(job.Status)
		buf.WriteString(fmt.Sprintf("    \"%s\" [label=\"%s\", fillcolor=%s, style=filled];\n", 
			job.ID, label, color))
	}
	
	buf.WriteString("\n")
	
	// Add edges
	for _, job := range plan.Jobs {
		for _, dep := range job.Dependencies {
			if dep != nil {
				buf.WriteString(fmt.Sprintf("    \"%s\" -> \"%s\";\n", dep.ID, job.ID))
			}
		}
	}
	
	buf.WriteString("}\n")
	
	return buf.String()
}

func generateASCIIGraph(plan *orchestration.Plan, graph *DependencyGraph) string {
	var buf strings.Builder
	
	// Simple ASCII representation
	buf.WriteString("Job Dependency Graph\n")
	buf.WriteString("===================\n\n")
	
	// Print jobs grouped by level
	levels := computeJobLevels(plan, graph)
	
	for level := 0; level <= getMaxLevel(levels); level++ {
		buf.WriteString(fmt.Sprintf("Level %d:\n", level))
		for jobID, jobLevel := range levels {
			if jobLevel == level {
				job := graph.Nodes[jobID]
				status := getStatusSymbol(job.Status)
				buf.WriteString(fmt.Sprintf("  [%s] %s %s\n", status, job.Filename, job.Title))
				
				// Show dependencies
				if len(job.Dependencies) > 0 {
					buf.WriteString("      └─ depends on: ")
					deps := []string{}
					for _, dep := range job.Dependencies {
						if dep != nil {
							deps = append(deps, dep.Filename)
						}
					}
					buf.WriteString(strings.Join(deps, ", "))
					buf.WriteString("\n")
				}
			}
		}
		buf.WriteString("\n")
	}
	
	// Add legend
	buf.WriteString("Legend:\n")
	buf.WriteString("  [✓] Completed\n")
	buf.WriteString("  [⚡] Running\n")
	buf.WriteString("  [⏳] Pending\n")
	buf.WriteString("  [✗] Failed\n")
	buf.WriteString("  [⊘] Blocked\n")
	buf.WriteString("  [?] Needs Review\n")
	
	return buf.String()
}

func computeJobLevels(plan *orchestration.Plan, graph *DependencyGraph) map[string]int {
	levels := make(map[string]int)
	
	// Initialize all jobs to level -1
	for _, job := range plan.Jobs {
		levels[job.ID] = -1
	}
	
	// Compute levels using BFS
	var computeLevel func(jobID string) int
	computeLevel = func(jobID string) int {
		if level, ok := levels[jobID]; ok && level >= 0 {
			return level
		}
		
		job := graph.Nodes[jobID]
		if job == nil {
			// This shouldn't happen, but handle gracefully
			levels[jobID] = 0
			return 0
		}
		
		if len(job.Dependencies) == 0 {
			levels[jobID] = 0
			return 0
		}
		
		maxDepLevel := -1
		for _, dep := range job.Dependencies {
			if dep == nil {
				continue
			}
			depLevel := computeLevel(dep.ID)
			if depLevel > maxDepLevel {
				maxDepLevel = depLevel
			}
		}
		
		levels[jobID] = maxDepLevel + 1
		return levels[jobID]
	}
	
	// Compute level for each job
	for _, job := range plan.Jobs {
		computeLevel(job.ID)
	}
	
	return levels
}

func getMaxLevel(levels map[string]int) int {
	max := 0
	for _, level := range levels {
		if level > max {
			max = level
		}
	}
	return max
}

type GraphPageData struct {
	PlanName     string
	Stats        GraphStats
	MermaidGraph string
}

type GraphStats struct {
	Total       int
	Completed   int
	Running     int
	Pending     int
	Failed      int
	Blocked     int
	NeedsReview int
}

func serveInteractiveGraph(plan *orchestration.Plan, graph *DependencyGraph, port int) error {
	// Calculate stats
	stats := GraphStats{Total: len(plan.Jobs)}
	for _, job := range plan.Jobs {
		switch job.Status {
		case orchestration.JobStatusCompleted:
			stats.Completed++
		case orchestration.JobStatusRunning:
			stats.Running++
		case orchestration.JobStatusPending:
			stats.Pending++
		case orchestration.JobStatusFailed:
			stats.Failed++
		case orchestration.JobStatusBlocked:
			stats.Blocked++
		case orchestration.JobStatusNeedsReview:
			stats.NeedsReview++
		}
	}

	// Generate page data
	pageData := GraphPageData{
		PlanName:     plan.Name,
		Stats:        stats,
		MermaidGraph: generateMermaidGraph(plan, graph),
	}

	// Parse template
	tmpl, err := template.New("graph").Parse(htmlTemplate)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	// Create handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, pageData); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write(buf.Bytes())
	})

	fmt.Printf("Serving graph at http://localhost:%d\n", port)
	fmt.Println("Press Ctrl+C to stop...")
	
	return http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
}

func getStatusSymbol(status orchestration.JobStatus) string {
	switch status {
	case orchestration.JobStatusCompleted:
		return "✓ Completed"
	case orchestration.JobStatusRunning:
		return "⚡ Running"
	case orchestration.JobStatusPending:
		return "⏳ Pending"
	case orchestration.JobStatusFailed:
		return "✗ Failed"
	case orchestration.JobStatusBlocked:
		return "⊘ Blocked"
	case orchestration.JobStatusNeedsReview:
		return "? Needs Review"
	default:
		return string(status)
	}
}

func getStatusClass(status orchestration.JobStatus) string {
	switch status {
	case orchestration.JobStatusCompleted:
		return "completed"
	case orchestration.JobStatusRunning:
		return "running"
	case orchestration.JobStatusPending:
		return "pending"
	case orchestration.JobStatusFailed:
		return "failed"
	case orchestration.JobStatusBlocked:
		return "blocked"
	case orchestration.JobStatusNeedsReview:
		return "needs_review"
	default:
		return "pending"
	}
}

func getStatusColor(status orchestration.JobStatus) string {
	switch status {
	case orchestration.JobStatusCompleted:
		return "lightgreen"
	case orchestration.JobStatusRunning:
		return "yellow"
	case orchestration.JobStatusPending:
		return "lightgray"
	case orchestration.JobStatusFailed:
		return "lightpink"
	case orchestration.JobStatusBlocked:
		return "plum"
	case orchestration.JobStatusNeedsReview:
		return "lightskyblue"
	default:
		return "white"
	}
}

const htmlTemplate = `<!DOCTYPE html>
<html>
<head>
    <title>Grove Jobs Graph - {{ .PlanName }}</title>
    <script src="https://cdn.jsdelivr.net/npm/mermaid/dist/mermaid.min.js"></script>
    <style>
        body { 
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; 
            margin: 0;
            padding: 20px;
            background-color: #f5f5f5;
        }
        .container {
            max-width: 1200px;
            margin: 0 auto;
            background-color: white;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
            padding: 20px;
        }
        h1 { 
            color: #333;
            margin-bottom: 20px;
        }
        #graph { 
            text-align: center;
            background-color: #fafafa;
            border-radius: 4px;
            padding: 20px;
            margin-top: 20px;
        }
        .stats { 
            background-color: #f0f0f0;
            padding: 15px;
            border-radius: 4px;
            margin-bottom: 20px;
        }
        .stats-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
            gap: 10px;
        }
        .stat-item {
            text-align: center;
            padding: 10px;
            background-color: white;
            border-radius: 4px;
        }
        .stat-value {
            font-size: 24px;
            font-weight: bold;
            margin-bottom: 5px;
        }
        .stat-label {
            font-size: 14px;
            color: #666;
        }
        .legend { 
            display: flex; 
            gap: 20px;
            flex-wrap: wrap;
            justify-content: center;
            margin-top: 20px;
        }
        .legend-item { 
            display: flex; 
            align-items: center; 
            gap: 5px;
            font-size: 14px;
        }
        .legend-color {
            width: 16px;
            height: 16px;
            border-radius: 3px;
            border: 1px solid rgba(0,0,0,0.2);
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>{{ .PlanName }} - Dependency Graph</h1>
        
        <div class="stats">
            <div class="stats-grid">
                <div class="stat-item">
                    <div class="stat-value">{{ .Stats.Total }}</div>
                    <div class="stat-label">Total Jobs</div>
                </div>
                <div class="stat-item">
                    <div class="stat-value" style="color: #228B22;">{{ .Stats.Completed }}</div>
                    <div class="stat-label">Completed</div>
                </div>
                <div class="stat-item">
                    <div class="stat-value" style="color: #FFA500;">{{ .Stats.Running }}</div>
                    <div class="stat-label">Running</div>
                </div>
                <div class="stat-item">
                    <div class="stat-value" style="color: #696969;">{{ .Stats.Pending }}</div>
                    <div class="stat-label">Pending</div>
                </div>
                {{ if gt .Stats.Failed 0 }}
                <div class="stat-item">
                    <div class="stat-value" style="color: #DC143C;">{{ .Stats.Failed }}</div>
                    <div class="stat-label">Failed</div>
                </div>
                {{ end }}
                {{ if gt .Stats.Blocked 0 }}
                <div class="stat-item">
                    <div class="stat-value" style="color: #8B008B;">{{ .Stats.Blocked }}</div>
                    <div class="stat-label">Blocked</div>
                </div>
                {{ end }}
                {{ if gt .Stats.NeedsReview 0 }}
                <div class="stat-item">
                    <div class="stat-value" style="color: #4682B4;">{{ .Stats.NeedsReview }}</div>
                    <div class="stat-label">Needs Review</div>
                </div>
                {{ end }}
            </div>
        </div>
        
        <div id="graph" class="mermaid">
{{ .MermaidGraph }}
        </div>
        
        <div class="legend">
            <div class="legend-item">
                <div class="legend-color" style="background-color: #90EE90;"></div>
                <span>Completed</span>
            </div>
            <div class="legend-item">
                <div class="legend-color" style="background-color: #FFD700;"></div>
                <span>Running</span>
            </div>
            <div class="legend-item">
                <div class="legend-color" style="background-color: #D3D3D3;"></div>
                <span>Pending</span>
            </div>
            <div class="legend-item">
                <div class="legend-color" style="background-color: #FFB6C1;"></div>
                <span>Failed</span>
            </div>
            <div class="legend-item">
                <div class="legend-color" style="background-color: #DDA0DD;"></div>
                <span>Blocked</span>
            </div>
            <div class="legend-item">
                <div class="legend-color" style="background-color: #87CEEB;"></div>
                <span>Needs Review</span>
            </div>
        </div>
    </div>
    
    <script>
        mermaid.initialize({ 
            startOnLoad: true,
            theme: 'default',
            flowchart: {
                useMaxWidth: true,
                htmlLabels: true
            }
        });
    </script>
</body>
</html>`