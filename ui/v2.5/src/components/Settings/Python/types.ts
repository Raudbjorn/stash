export type PythonJobImpact = "lifecycle" | "packages" | "catalogs";

export interface ActivePythonJob {
  id: string;
  impact: PythonJobImpact;
}

export type StartPythonJob = (
  id: string,
  impact: PythonJobImpact
) => void;
