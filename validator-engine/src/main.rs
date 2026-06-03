mod pb;

use pb::simulation::validator_service_server::{ValidatorService, ValidatorServiceServer};
use pb::simulation::{ValidationRequest, ValidationResponse};
use tonic::{transport::Server, Request, Response, Status};
use std::env;
use dotenvy::dotenv;

#[derive(Debug, Default)]
pub struct MyValidatorService {}

#[tonic::async_trait]
impl ValidatorService for MyValidatorService {
    async fn validate_job(
        &self,
        request: Request<ValidationRequest>,
    ) -> Result<Response<ValidationResponse>, Status> {
        let req = request.into_inner();
        
        // 1. Binding Energy Check (Tolerance: 0.01 kcal/mol)
        let energy_diff = (req.result_a_energy - req.result_b_energy).abs();
        if energy_diff > 0.01 {
            println!("[FRAUD DETECTED] Job {}: Energy difference too high ({} kcal/mol)", req.job_id, energy_diff);
            return Ok(Response::new(ValidationResponse {
                is_valid: false,
                message: format!("FRAUD: Energy difference too high ({} kcal/mol)", energy_diff),
            }));
        }

        // 2. PDBQT Coordinates Check (Tolerance: 0.1 Angstroms)
        let is_coords_valid = compare_pdbqt_coords(&req.result_a_pdbqt, &req.result_b_pdbqt);
        if !is_coords_valid {
             println!("[FRAUD DETECTED] Job {}: Coordinate deviation exceeded tolerance.", req.job_id);
             return Ok(Response::new(ValidationResponse {
                is_valid: false,
                message: "FRAUD: Coordinate deviation exceeded tolerance.".into(),
            }));
        }

        println!("[VALIDATED] Job {}: Results match successfully.", req.job_id);
        Ok(Response::new(ValidationResponse {
            is_valid: true,
            message: "VALID".into(),
        }))
    }
}

// Simple parser to extract and compare atomic coordinates
fn compare_pdbqt_coords(data_a: &[u8], data_b: &[u8]) -> bool {
    let str_a = String::from_utf8_lossy(data_a);
    let str_b = String::from_utf8_lossy(data_b);

    let coords_a = extract_coords(&str_a);
    let coords_b = extract_coords(&str_b);

    // If both files have no coordinates, they are identical (or both corrupted, which fails spot check anyway)
    if coords_a.is_empty() || coords_a.len() != coords_b.len() {
        return false;
    }

    // Check distance between each atom
    let tolerance = 0.1; // 0.1 Angstroms
    for (a, b) in coords_a.iter().zip(coords_b.iter()) {
        let dist_sq = (a.0 - b.0).powi(2) + (a.1 - b.1).powi(2) + (a.2 - b.2).powi(2);
        if dist_sq > tolerance * tolerance {
            return false;
        }
    }

    true
}

fn extract_coords(pdbqt: &str) -> Vec<(f64, f64, f64)> {
    let mut coords = Vec::new();
    for line in pdbqt.lines() {
        if line.starts_with("ATOM") || line.starts_with("HETATM") {
            // PDB format coordinates are at specific column indices:
            // X: 30-38, Y: 38-46, Z: 46-54
            if line.len() >= 54 {
                let x_str = line[30..38].trim();
                let y_str = line[38..46].trim();
                let z_str = line[46..54].trim();

                if let (Ok(x), Ok(y), Ok(z)) = (x_str.parse::<f64>(), y_str.parse::<f64>(), z_str.parse::<f64>()) {
                    coords.push((x, y, z));
                }
            }
        }
    }
    coords
}

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    dotenv().ok();
    
    // Defaulting to 50052 for Validator Engine to not clash with Go Backend (50051)
    let port = env::var("VALIDATOR_PORT").unwrap_or_else(|_| "50052".to_string());
    let addr = format!("0.0.0.0:{}", port).parse()?;
    let validator = MyValidatorService::default();

    println!("Validator Engine gRPC server listening on {}", addr);

    Server::builder()
        .add_service(ValidatorServiceServer::new(validator))
        .serve(addr)
        .await?;

    Ok(())
}
